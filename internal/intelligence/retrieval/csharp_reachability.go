package retrieval

import (
	"github.com/asqs/asqs-core/internal/dotnetproj"
	"github.com/asqs/asqs-core/internal/langid"
)

// reachabilityFilter answers whether a source file's symbols can be reached from the repository's
// test projects. A zero value allows everything, which is what every language but C# gets and what
// C# gets when the working tree cannot be read.
type reachabilityFilter struct {
	dirs []string
}

func (f reachabilityFilter) allows(file string) bool {
	if len(f.dirs) == 0 {
		return true
	}
	return dotnetproj.PathIsReachableFrom(file, f.dirs)
}

// csharpReachableFilter builds the set of directories a C# test can reference.
//
// A test cannot name a type its project does not reference, however well it is written. The
// validation run of 2026-09-12 discarded four of nine gaps for exactly that: the test project
// referenced Shop.Core and not Shop, so every controller and Razor Pages gap was doomed before
// generation — and nothing noticed until the compiler said so, after each had spent its generation
// and its fix rounds.
//
// Three conditions must all hold before anything is filtered, because a wrong exclusion silently
// removes work the run should have done:
//
//   - the language is C#, which is the only one whose reachability this can compute;
//   - the repository path is available, so the project files can actually be read; and
//   - at least one test project exists AND every one of their closures came back. A repository with
//     no test project yet is the bootstrap case, where everything is reachable once one is created.
//
// The third condition was decorative when it was written: TestProjectReachableDirs recorded the
// test project's own directory before reading anything, so its result was never empty and the only
// live guard was "no test project at all". It answers for something now — that function returns
// nothing at all when it cannot read a closure in full, which is what an inherited or
// property-built ProjectReference produces.
//
// EVERY closure, not at least one. The union across test projects is the right answer for what is
// reachable — a symbol reachable from any test project is a symbol some test can cover — but it is
// the wrong answer for what is UNKNOWN. A solution with two test projects where one of them cannot
// be read produces a union that is complete for the project that could be read and empty for the
// one that could not, and the difference is invisible: every gap under the second project's
// production code is excluded with nothing to say so. Two test projects is the normal shape once a
// solution has more than one component. One unreadable closure therefore disables the filter
// outright, which is the same rule the function itself applies one level down.
//
// The cost of being wrong in this direction is a planned gap whose test cannot reference its
// subject, which the compiler reports in the run. The cost of the other direction is silence.
func csharpReachableFilter(opts PlanOptions) reachabilityFilter {
	if !langid.IsCSharp(opts.Lang) || opts.RepoPath == "" {
		return reachabilityFilter{}
	}
	testProjects := dotnetproj.FindTestProjects(opts.RepoPath)
	if len(testProjects) == 0 {
		return reachabilityFilter{}
	}
	seen := map[string]bool{}
	var dirs []string
	for _, proj := range testProjects {
		closure := dotnetproj.TestProjectReachableDirs(opts.RepoPath, proj)
		if closure == nil {
			return reachabilityFilter{}
		}
		for _, d := range closure {
			if !seen[d] {
				seen[d] = true
				dirs = append(dirs, d)
			}
		}
	}
	if len(dirs) == 0 {
		return reachabilityFilter{}
	}
	return reachabilityFilter{dirs: dirs}
}
