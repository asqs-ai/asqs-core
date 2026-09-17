package testbootstrap

import (
	"github.com/asqs/asqs-core/internal/dotnetproj"
)

// Solution parsing lives in dotnetproj: internal/layout needs the same answer, and a second copy
// here is how the bootstrap's project choice and generation's came to disagree in the first place.

func discoverRootSolutionFilePaths(repo string) ([]string, error) {
	return dotnetproj.RootSolutionFilePaths(repo)
}

func collectCsprojPathsFromRootSolutions(repo string) ([]string, error) {
	return dotnetproj.CsprojPathsFromRootSolutions(repo)
}
