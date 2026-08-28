package apisurface

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Provider resolves API surfaces for a repository. Implementations must be safe for concurrent use
// and must never fail a run: a lookup that cannot be satisfied returns no surfaces and an error the
// caller audits, and the fixer proceeds without the block.
type Provider interface {
	// Lookup returns surfaces for as many targets as it can resolve, in target order.
	//
	// The error is the "I could not check" signal — no classpath, no type declarations, no
	// documentation — and is distinct from an empty result, which means the provider checked its
	// inputs and the target is not in them. Callers that want to make a claim about ABSENCE must
	// honour that distinction; see CanProveTypeAbsence.
	Lookup(ctx context.Context, repoPath string, targets []Target) ([]TypeSurface, error)
}

// AbsenceProver is implemented by a Provider whose empty result for a KindType target proves the
// type is not available to the compiler, as opposed to merely not being documented in whatever
// index that provider happens to read.
//
// The distinction is the whole point. javap is asked against the resolved test compile classpath —
// the exact set of types javac itself will see — so "javap found nothing" is the same fact the
// compiler will report. The C# provider reads NuGet XML documentation and the Node provider reads
// bundled .d.ts files; a type can be perfectly real and simply absent from either, so an empty
// result there is not evidence of anything and must not be reported as such.
//
// Opting in is therefore a claim about the provider's INPUTS, not about its diligence: implement
// this only when the thing consulted is the same thing the compiler consults.
type AbsenceProver interface {
	ProvesTypeAbsence() bool
}

// CanProveTypeAbsence reports whether an empty Lookup result from p, for a KindType target, is
// strong enough to tell a model that the type does not exist.
func CanProveTypeAbsence(p Provider) bool {
	prover, ok := p.(AbsenceProver)
	return ok && prover.ProvesTypeAbsence()
}

// ProvesTypeAbsence implements AbsenceProver: javap is asked against the classpath javac compiles
// with, so a miss is the compiler's own answer.
func (p *JavaProvider) ProvesTypeAbsence() bool { return p != nil }

// classpathTTL bounds how long a resolved classpath is reused. The classpath is stable across fixer
// rounds and usually across runs; re-resolving per round would add a Maven invocation to every one.
const classpathTTL = 30 * time.Minute

// resolveTimeout bounds the Maven classpath resolution. Past this the fixer is better off with no
// API block than with a round that stalls behind a dependency download.
var resolveTimeout = 3 * time.Minute

// javapTimeout bounds a single javap invocation.
var javapTimeout = 20 * time.Second

// JavaProvider extracts API surfaces for Maven/Gradle Java projects using the JDK tools that ship
// alongside the compiler the evaluator already runs: `javap` for member lists and the jar index for
// unresolved simple names.
type JavaProvider struct {
	// MavenCommand overrides the Maven executable. Empty uses the repo's wrapper when present,
	// else "mvn".
	MavenCommand string

	mu        sync.Mutex
	cpCache   map[string]classpathEntry // repoPath -> classpath
	typeCache map[string]TypeSurface    // fingerprint\x00fqcn -> surface
	// cpGroup collapses concurrent first-time resolutions of the same repo into one Maven run.
	// The generator used to hold a sync.Once around its single Lookup, which serialised callers
	// as a side effect; now that the block is resolved per gap, the gap fan-out (gap_concurrency
	// defaults above one) reaches this method concurrently and the cache check below cannot stop
	// it — it unlocks before resolving, so N callers would each start `mvn dependency:build-classpath`
	// against the same working copy.
	cpGroup singleflight.Group
}

type classpathEntry struct {
	classpath   string
	fingerprint string
	at          time.Time
	err         error
}

// NewJavaProvider constructs a provider with empty caches.
func NewJavaProvider() *JavaProvider {
	return &JavaProvider{
		cpCache:   map[string]classpathEntry{},
		typeCache: map[string]TypeSurface{},
	}
}

// Lookup implements Provider.
func (p *JavaProvider) Lookup(ctx context.Context, repoPath string, targets []Target) ([]TypeSurface, error) {
	if p == nil || len(targets) == 0 || strings.TrimSpace(repoPath) == "" {
		return nil, nil
	}
	cp, fingerprint, err := p.classpath(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cp) == "" {
		return nil, fmt.Errorf("apisurface: empty compile classpath")
	}
	var out []TypeSurface
	for _, t := range targets {
		switch t.Kind {
		case KindType:
			s, err := p.typeSurface(ctx, fingerprint, cp, t)
			if err != nil || s.FQCN == "" {
				continue
			}
			out = append(out, s)
		case KindSymbol:
			out = append(out, p.resolveSymbol(cp, t)...)
		}
	}
	return out, nil
}

// classpath resolves (and caches) the test compile classpath for repoPath.
//
// Resolution is single-flighted per repoPath: the cache check cannot be held across the Maven
// invocation without serialising every later cache HIT behind it, so concurrent misses are
// collapsed by cpGroup instead. Losers wait for the winner and read its result.
func (p *JavaProvider) classpath(ctx context.Context, repoPath string) (cp, fingerprint string, err error) {
	if e, ok := p.cachedClasspath(repoPath); ok {
		return e.classpath, e.fingerprint, e.err
	}
	v, _, _ := p.cpGroup.Do(repoPath, func() (interface{}, error) {
		// Re-check under the flight: a winner that finished while this caller was queuing has
		// already populated the cache, and re-running Maven for it would defeat the point.
		if e, ok := p.cachedClasspath(repoPath); ok {
			return e, nil
		}
		resolved, rerr := p.resolveMavenClasspath(ctx, repoPath)
		fp := ""
		if rerr == nil {
			sum := sha256.Sum256([]byte(resolved))
			fp = hex.EncodeToString(sum[:8])
		}
		e := classpathEntry{classpath: resolved, fingerprint: fp, at: time.Now(), err: rerr}
		p.mu.Lock()
		p.cpCache[repoPath] = e
		p.mu.Unlock()
		// Returned as a value, never as the flight's error: singleflight would hand the same
		// error to every waiter, and the entry already carries it.
		return e, nil
	})
	e, ok := v.(classpathEntry)
	if !ok {
		return "", "", fmt.Errorf("apisurface: classpath resolution produced no result for %s", repoPath)
	}
	return e.classpath, e.fingerprint, e.err
}

// cachedClasspath returns a live cache entry for repoPath.
func (p *JavaProvider) cachedClasspath(repoPath string) (classpathEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e, ok := p.cpCache[repoPath]; ok && time.Since(e.at) < classpathTTL {
		return e, true
	}
	return classpathEntry{}, false
}

// resolveMavenClasspath asks Maven for the test classpath. It writes to a temp file rather than
// parsing stdout, because Maven interleaves plugin output with the value and a stdout parse breaks
// the first time a plugin logs a line containing a path separator.
func (p *JavaProvider) resolveMavenClasspath(ctx context.Context, repoPath string) (string, error) {
	if _, err := os.Stat(filepath.Join(repoPath, "pom.xml")); err != nil {
		return "", fmt.Errorf("apisurface: no pom.xml under %s (only Maven is supported today)", repoPath)
	}
	out, err := os.CreateTemp("", "asqs-cp-*.txt")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	out.Close()
	defer os.Remove(outPath)

	cctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, p.mavenCommand(repoPath),
		"-q", "-o", "-B",
		"dependency:build-classpath",
		"-Dmdep.includeScope=test",
		"-Dmdep.outputFile="+outPath,
	)
	cmd.Dir = repoPath
	combined, runErr := cmd.CombinedOutput()
	b, readErr := os.ReadFile(outPath)
	if readErr != nil || strings.TrimSpace(string(b)) == "" {
		// Offline mode fails when the local repository is cold. Retry online once: the jars are
		// normally already cached (the compile step just ran), so this is the uncommon path.
		cmd = exec.CommandContext(cctx, p.mavenCommand(repoPath),
			"-q", "-B",
			"dependency:build-classpath",
			"-Dmdep.includeScope=test",
			"-Dmdep.outputFile="+outPath,
		)
		cmd.Dir = repoPath
		combined, runErr = cmd.CombinedOutput()
		b, readErr = os.ReadFile(outPath)
	}
	if readErr != nil || strings.TrimSpace(string(b)) == "" {
		msg := strings.TrimSpace(string(combined))
		if len(msg) > 400 {
			msg = msg[:400] + "…"
		}
		if runErr != nil {
			return "", fmt.Errorf("apisurface: dependency:build-classpath failed: %v: %s", runErr, msg)
		}
		return "", fmt.Errorf("apisurface: dependency:build-classpath produced no classpath: %s", msg)
	}
	// Include the project's own compiled classes so repo types resolve too.
	cp := strings.TrimSpace(string(b))
	for _, d := range []string{"target/classes", "target/test-classes"} {
		full := filepath.Join(repoPath, filepath.FromSlash(d))
		if st, err := os.Stat(full); err == nil && st.IsDir() {
			cp = full + string(os.PathListSeparator) + cp
		}
	}
	return cp, nil
}

func (p *JavaProvider) mavenCommand(repoPath string) string {
	if c := strings.TrimSpace(p.MavenCommand); c != "" {
		return c
	}
	// D3 (U3b): the PATH binary, never a repo wrapper. This also drops a fifth home-grown host
	// check — it detected Windows via os.PathSeparator and ignored general.build.build_tool entirely.
	return "mvn"
}

// typeSurface runs javap for one fully-qualified type, with a per-classpath cache.
func (p *JavaProvider) typeSurface(ctx context.Context, fingerprint, cp string, t Target) (TypeSurface, error) {
	key := fingerprint + "\x00" + t.Name
	p.mu.Lock()
	if s, ok := p.typeCache[key]; ok {
		p.mu.Unlock()
		// The cache holds the COMPLETE member list; the ranked view is per-target because it is
		// ranked against the member this diagnostic rejected.
		return NewTypeSurface(s.FQCN, s.Members, t.Member, s.Origin), nil
	}
	p.mu.Unlock()

	jctx, cancel := context.WithTimeout(ctx, javapTimeout)
	defer cancel()
	cmd := exec.CommandContext(jctx, "javap", "-cp", cp, t.Name)
	raw, err := cmd.Output()
	if err != nil {
		return TypeSurface{}, fmt.Errorf("apisurface: javap %s: %w", t.Name, err)
	}
	members := parseJavapMembers(string(raw))
	if len(members) == 0 {
		return TypeSurface{}, fmt.Errorf("apisurface: javap %s produced no members", t.Name)
	}
	origin := originOf(cp, t.Name)

	p.mu.Lock()
	p.typeCache[key] = TypeSurface{FQCN: t.Name, Members: members, Origin: origin}
	p.mu.Unlock()

	return NewTypeSurface(t.Name, members, t.Member, origin), nil
}

// parseJavapMembers keeps the declaration lines and drops javap's header/footer.
func parseJavapMembers(raw string) []string {
	var out []string
	for _, ln := range strings.Split(raw, "\n") {
		s := strings.TrimSpace(ln)
		if s == "" || s == "}" || strings.HasPrefix(s, "Compiled from") {
			continue
		}
		// The type declaration line itself ends in "{"; members end in ";".
		if strings.HasSuffix(s, "{") {
			continue
		}
		if !strings.HasSuffix(s, ";") {
			continue
		}
		out = append(out, s)
	}
	return out
}

// resolveSymbol finds classpath entries that could provide an unresolved simple name, so the fixer
// can add the right import instead of inventing one.
func (p *JavaProvider) resolveSymbol(cp string, t Target) []TypeSurface {
	want := "/" + t.Name + ".class"
	var found []string
	for _, entry := range strings.Split(cp, string(os.PathListSeparator)) {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		for _, cls := range classEntriesMatching(entry, want) {
			fq := strings.TrimSuffix(strings.ReplaceAll(cls, "/", "."), ".class")
			found = append(found, fq)
		}
		if len(found) >= 8 {
			break
		}
	}
	sort.Strings(found)
	out := make([]TypeSurface, 0, len(found))
	for _, fq := range found {
		out = append(out, TypeSurface{FQCN: fq})
	}
	return out
}

func originOf(cp, fqcn string) string {
	want := "/" + fqcn[strings.LastIndex(fqcn, ".")+1:] + ".class"
	for _, entry := range strings.Split(cp, string(os.PathListSeparator)) {
		if len(classEntriesMatching(strings.TrimSpace(entry), want)) > 0 {
			return filepath.Base(entry)
		}
	}
	return ""
}
