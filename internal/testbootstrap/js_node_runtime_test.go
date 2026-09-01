package testbootstrap

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// testNodeVersion is the Node runtime handed to buildJSTestProfile wherever a test is not about the
// runtime itself. It is a current LTS patch that satisfies every jsdom line, so those tests keep
// exercising the newest stack; the Node-sensitive cases pass their own version explicitly.
const testNodeVersion = "v22.23.2"

func TestNodeSemver(t *testing.T) {
	cases := []struct {
		in                  string
		major, minor, patch int
	}{
		{"v20.20.2", 20, 20, 2},
		{"20.20.2", 20, 20, 2},
		{"  v22.23.2\n", 22, 23, 2},
		{"v24.0.0-nightly20260101", 24, 0, 0},
		{"v26", 26, 0, 0},
		{"", 0, 0, 0},
		{"not-a-version", 0, 0, 0},
	}
	for _, tc := range cases {
		major, minor, patch := nodeSemver(tc.in)
		if major != tc.major || minor != tc.minor || patch != tc.patch {
			t.Errorf("nodeSemver(%q) = %d.%d.%d, want %d.%d.%d", tc.in, major, minor, patch, tc.major, tc.minor, tc.patch)
		}
	}
}

// TestJsdomVersionForNode pins the selector to each release's declared engines.node, including the
// boundaries: jsdom 30 requires ^22.22.2 || ^24.15.0 || >=26.0.0, jsdom 29 requires
// ^20.19.0 || ^22.13.0 || >=24.0.0, and jsdom 26 requires >=18.
func TestJsdomVersionForNode(t *testing.T) {
	cases := []struct {
		node    string
		want    string
		wantOK  bool
		because string
	}{
		{"v20.20.2", VersionJsdom29, true, "node:20-bookworm-slim, where jsdom 30 cannot load at all"},
		{"v20.19.0", VersionJsdom29, true, "the first Node 20 patch jsdom 29 declares"},
		{"v20.18.0", VersionJsdom26, true, "below jsdom 29's ^20.19.0 floor"},
		{"v22.13.0", VersionJsdom29, true, "jsdom 30 needs ^22.22.2, jsdom 29 accepts ^22.13.0"},
		{"v22.22.1", VersionJsdom29, true, "one patch below jsdom 30's ^22.22.2 floor"},
		{"v22.22.2", VersionJsdom30, true, "exactly jsdom 30's Node 22 floor"},
		{"22.23.2", VersionJsdom30, true, "the probe's own format — process.versions.node has no v prefix"},
		{"v23.11.0", VersionJsdom26, true, "no current jsdom line covers the odd major 23"},
		{"v24.14.0", VersionJsdom29, true, "jsdom 30 declares ^24.15.0, so 24.14 stays on 29"},
		{"v24.15.0", VersionJsdom30, true, "exactly jsdom 30's Node 24 floor"},
		{"v26.0.0", VersionJsdom30, true, "jsdom 30 covers >=26"},
		{"v18.20.8", VersionJsdom26, true, "only the pre-undici line supports Node 18"},
		{"", VersionJsdom26, true, "an unreadable probe falls back to the widest line, not the newest"},
		{"v16.20.2", "", false, "older than every current jsdom line"},
	}
	for _, tc := range cases {
		got, ok := jsdomVersionForNode(tc.node)
		if ok != tc.wantOK {
			t.Errorf("jsdomVersionForNode(%q) ok = %v, want %v (%s)", tc.node, ok, tc.wantOK, tc.because)
			continue
		}
		if got != tc.want {
			t.Errorf("jsdomVersionForNode(%q) = %q, want %q (%s)", tc.node, got, tc.want, tc.because)
		}
	}
}

// The regression: jsdom 30 pinned into a Node 20 container installs (npm's engines is only a
// warning) and then fails at require() with "webidl.util.markAsUncloneable is not a function",
// which kills the runner before a single test runs.
func TestBuildJSTestProfile_jsdomLineFollowsNodeRuntime(t *testing.T) {
	dets := map[string]jsFrameworkDetection{
		"vitest-react":      {Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, IsTS: true},
		"vitest-vue":        {Framework: JSFrameworkVue, FrameworkMajor: 3, ViteMajor: 6, IsTS: true},
		"vitest-svelte":     {Framework: JSFrameworkSvelte, FrameworkMajor: 5, ViteMajor: 6, IsTS: true},
		"vitest-vite-jsdom": {Framework: JSFrameworkPlain, ViteMajor: 6, BrowserLike: true, IsTS: true},
	}
	for stack, det := range dets {
		t.Run(stack, func(t *testing.T) {
			node20 := buildJSTestProfile(det, "v20.20.2")
			if got := jsDepVersion(node20, "jsdom"); got != VersionJsdom29 {
				t.Errorf("Node 20 → jsdom %q, want %q: the 30 line cannot load there", got, VersionJsdom29)
			}
			if node20.Declined {
				t.Errorf("Node 20 hosts jsdom 29 fine; declined with %q", node20.DeclinedReason)
			}
			if got := jsDepVersion(buildJSTestProfile(det, "v22.23.2"), "jsdom"); got != VersionJsdom30 {
				t.Errorf("Node 22.23 → jsdom %q, want %q", got, VersionJsdom30)
			}
		})
	}
}

func TestBuildJSTestProfile_declinesDOMStackOnUnsupportedNode(t *testing.T) {
	const ancient = "v16.20.2"

	dom := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, ViteMajor: 6, IsTS: true}, ancient)
	if !dom.Declined {
		t.Fatalf("a jsdom stack on Node 16 must decline, got stack %q deps %v", dom.Stack, describeJSDeps(dom.Deps))
	}
	if dom.Stack != jsStackJsdomDeclined {
		t.Errorf("stack = %q, want %q so the audit can separate a runtime fault from a repo one", dom.Stack, jsStackJsdomDeclined)
	}
	for _, want := range []string{ancient, "images.node"} {
		if !strings.Contains(dom.DeclinedReason, want) {
			t.Errorf("decline reason must name %q so it is actionable: %s", want, dom.DeclinedReason)
		}
	}

	// A stack that never installs jsdom is not the runtime's business.
	if node := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkPlain, IsTS: true}, ancient); node.Declined {
		t.Errorf("a node-environment stack needs no jsdom; declined with %q", node.DeclinedReason)
	}
	// Jest's DOM stack gets jsdom from jest-environment-jsdom, not from a jsdom dep of ours.
	jest := buildJSTestProfile(jsFrameworkDetection{Framework: JSFrameworkReact, FrameworkMajor: 18, IsTS: true}, ancient)
	if jest.Declined {
		t.Errorf("the Jest DOM stack pins no jsdom of its own; declined with %q", jest.DeclinedReason)
	}
	if v := jsDepVersion(jest, "jsdom"); v != "" {
		t.Errorf("jest stack must not carry a jsdom dep, got %q", v)
	}
}

// The probe reads the runtime it is pointed at. ed is nil here, so it runs on the host; the Docker
// path uses the same RunArgv call.
func TestDetectNodeVersion_host(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not on PATH")
	}
	got := detectNodeVersion(context.Background(), nil, t.TempDir())
	if got == "" {
		t.Fatal("probe returned nothing for a host that has node")
	}
	if major, _, _ := nodeSemver(got); major == 0 {
		t.Fatalf("probe returned %q, which nodeSemver cannot read", got)
	}
	want, err := exec.Command("node", "-p", "process.versions.node").Output()
	if err != nil {
		t.Fatalf("node -p failed: %v", err)
	}
	if got != strings.TrimSpace(string(want)) {
		t.Errorf("probe = %q, host node = %q", got, strings.TrimSpace(string(want)))
	}
}

// jsDepVersion returns the pinned version of one dep, or "" when the profile does not carry it.
func jsDepVersion(p jsTestProfile, name string) string {
	for _, d := range p.Deps {
		if d.Name == name {
			return d.Version
		}
	}
	return ""
}
