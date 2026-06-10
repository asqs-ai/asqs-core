// Package azuredevops implements Azure DevOps Repos REST integration (pull requests, threads, commit statuses).
//
// HTTPS Git operations (clone, fetch, push) for dev.azure.com are implemented in internal/repo using the git CLI
// and http.extraHeader PAT auth; see docs/AZURE_DEVOPS.md in the repository.
package azuredevops
