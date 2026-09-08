package testbootstrap

import (
	"strings"
	"testing"
)

// Run api-d01f66ab5b4c58f8e129844d98f8e370: nest build emitted dist/__tests__/asqs-bootstrap-
// smoke.test.d.ts, the __tests__ glob matched it, and every evaluation failed on a declaration
// file no fixer could repair. Build output must be outside Jest's reach.
func TestRenderJestConfig_ignoresBuildOutput(t *testing.T) {
	for _, p := range []jsTestProfile{
		{IsTS: true, TestEnvironment: "node", Framework: JSFrameworkNest},
		{IsTS: false, TestEnvironment: "jsdom"},
		{IsTS: true, TestEnvironment: "jsdom", Framework: JSFrameworkAngular},
	} {
		cfg := renderJestConfig(p)
		for _, want := range []string{"'<rootDir>/dist/'", "'<rootDir>/build/'", "'<rootDir>/out/'", "'/node_modules/'", "'<rootDir>/e2e/'", "'<rootDir>/cypress/'"} {
			if !strings.Contains(cfg, want) {
				t.Errorf("profile %+v: jest config lacks %s:\n%s", p, want, cfg)
			}
		}
		if strings.Count(cfg, "testPathIgnorePatterns") != 1 {
			t.Errorf("profile %+v: expected exactly one testPathIgnorePatterns:\n%s", p, cfg)
		}
	}
}
