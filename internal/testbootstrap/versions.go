package testbootstrap

// Pinned dependency versions for reproducible Jest bootstrap (update periodically).
const (
	VersionJest      = "29.7.0"
	VersionTSJest    = "29.2.5"
	VersionTypesJest = "29.5.14"
	VersionTypesNode = "22.10.0"
	// @playwright/test (pin for e2e_framework_bootstrap).
	VersionPlaywrightTest = "1.49.1"
	// DefaultPlaywrightDockerImage is mcr.microsoft.com/playwright for JS/TS E2E bootstrap in Docker (keep patch in sync with VersionPlaywrightTest).
	DefaultPlaywrightDockerImage = "mcr.microsoft.com/playwright:v1.49.1-jammy"
	// cypress (pin for e2e_framework_bootstrap).
	VersionCypress = "13.16.1"
)

// Pinned JUnit 5 / Surefire / Platform for Java bootstrap.
const (
	VersionJUnitJupiter        = "5.11.3"
	VersionJUnitPlatform       = "1.11.3"
	VersionMavenSurefirePlugin = "3.5.2"
	// Playwright Java (e2e_framework_bootstrap for JVM); align major with JS @playwright/test when possible.
	VersionPlaywrightJava = "1.49.0"
	// DefaultPlaywrightJavaDockerImage is mcr.microsoft.com/playwright/java for Java E2E bootstrap in Docker
	// (JDK + Maven + baked browsers + OS libs). Keep tag aligned with VersionPlaywrightJava.
	DefaultPlaywrightJavaDockerImage = "mcr.microsoft.com/playwright/java:v1.49.0-jammy"
)

// Microsoft.Playwright NuGet (e2e_framework_bootstrap for .NET). Keep aligned with runner.DefaultPlaywrightDotnetDockerImage tag.
const VersionMicrosoftPlaywrightNuGet = "1.49.0"

// Pinned .NET test packages for C# bootstrap (SDK-style csproj).
const (
	VersionDotNetTestSDK = "17.12.0"
	VersionXunit         = "2.9.2"
	VersionXunitRunnerVS = "2.8.2"
)
