package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/asqs/asqs-core/internal/buildtool"
)

// warnInertDockerKeysForLocalRunner warns about sandbox keys that ONLY the Docker eval path reads,
// when general.sandbox.type is not docker.
//
// Most Docker-only keys are harmless when ignored — a local run simply uses the host's own ~/.m2
// instead of general.sandbox.caches.maven. require_docker_bootstrap is different: it is a hard runtime
// error rather than a silent no-op, but only once an install step actually runs — so a config that
// pairs it with a local runner looks fine until the bootstrap phase fails mid-run. Say it at
// startup instead.
//
// A warning rather than an error: shared configs legitimately carry both Docker and local
// settings, and refusing to start would break them.
func warnInertDockerKeysForLocalRunner(c *Config) {
	if c == nil || strings.EqualFold(strings.TrimSpace(c.Runner.Type), "docker") {
		return
	}
	runnerType := strings.TrimSpace(c.Runner.Type)
	if runnerType == "" {
		runnerType = "local"
	}
	if c.Runner.RequireDockerBootstrap &&
		(c.Runner.TestFrameworkBootstrap.Enabled || c.Runner.E2EFrameworkBootstrap.Enabled) {
		fmt.Fprintf(os.Stderr,
			"config: bootstrap.require_docker is true but general.sandbox.type is %q, so bootstrap would run on the host — "+
				"the bootstrap step will fail. Set general.sandbox.type to docker, set a stage's execution to docker, "+
				"or set bootstrap.require_docker to false.\n", runnerType)
	}
}

// warnDeprecatedBuildToolWrapperAlias reports a general.build.build_tool of "mvnw" or "gradlew".
//
// CP32 removed repository build wrappers from every part of the pipeline, so these values no
// longer select anything different — they are canonicalised to "mvn" / "gradle". Warning rather
// than erroring keeps existing configs starting; the value they asked for is simply no longer a
// distinct choice.
func warnDeprecatedBuildToolWrapperAlias(c *Config) {
	if c == nil {
		return
	}
	canonical, wasAlias, ok := buildtool.Canonicalize(c.Runner.BuildTool)
	if !ok {
		fmt.Fprintf(os.Stderr,
			"config: general.build.build_tool is %q; valid values are auto, mvn and gradle "+
				"(mvnw and gradlew are deprecated aliases). Evaluation will fail when it resolves the build tool.\n",
			strings.TrimSpace(c.Runner.BuildTool))
		return
	}
	if !wasAlias {
		return
	}
	fmt.Fprintf(os.Stderr,
		"config: general.build.build_tool is %q, which is deprecated and now means %q. Repository build "+
			"wrappers (./mvnw, ./gradlew) are no longer invoked anywhere in the pipeline, so the "+
			"build tool comes from PATH on a local runner and from the toolchain image under docker. "+
			"Set general.build.build_tool to %q to silence this.\n",
		strings.TrimSpace(c.Runner.BuildTool), canonical, canonical)
}

// normaliseAndValidateRunnerType canonicalises general.sandbox.type and rejects anything unrecognised.
//
// Normalising first means "Docker" and " docker " behave as "docker" everywhere, rather than only
// where a caller remembered to fold case.
//
// The rejection matters more than it looks. An unrecognised value used to be accepted silently,
// and every evaluation step then returned {OK: true, Summary: "stub"} — so `general.sandbox.type: dcoker`
// green-lit a run that compiled nothing, tested nothing, and shipped. Startup is where an operator
// can still act on it.
func normaliseAndValidateRunnerType(c *Config) error {
	if c == nil {
		return nil
	}
	c.Runner.Type = strings.ToLower(strings.TrimSpace(c.Runner.Type))
	if c.Runner.Type == "" {
		c.Runner.Type = "local"
	}
	if c.Runner.Type != "local" && c.Runner.Type != "docker" {
		return fmt.Errorf("config validation: general.sandbox.type must be \"local\" or \"docker\"; got %q", c.Runner.Type)
	}
	return nil
}
