package apisurface

import (
	"context"
	"strings"
	"testing"
)

const nodeRepo = "testdata/node_repo"

func memberNamed(members []string, name string) bool {
	for _, m := range members {
		if memberName(m) == name {
			return true
		}
	}
	return false
}

func surfaceFor(t *testing.T, surfaces []TypeSurface, fqcn string) TypeSurface {
	t.Helper()
	for _, s := range surfaces {
		if s.FQCN == fqcn {
			return s
		}
	}
	t.Fatalf("no surface for %s (got %d surfaces)", fqcn, len(surfaces))
	return TypeSurface{}
}

// The premise of the whole mechanism: APIResponseAssertions really does have two members, so a
// model emitting hasStatus/hasHeader (Java) or toHaveStatus (TS) is contradicted by the file the
// project already has on disk.
func TestNodeProvider_resolvesAssertionInterfaces(t *testing.T) {
	p := NewNodeProvider()
	got, err := p.Lookup(context.Background(), nodeRepo, []Target{
		{Kind: KindType, Name: "APIResponseAssertions"},
		{Kind: KindType, Name: "LocatorAssertions"},
		{Kind: KindType, Name: "PageAssertions"},
	})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resolved %d surfaces, want 3: %+v", len(got), got)
	}

	api := surfaceFor(t, got, "APIResponseAssertions")
	if len(api.Members) != 2 {
		t.Errorf("APIResponseAssertions has %d members, want 2: %v", len(api.Members), api.Members)
	}
	if !memberNamed(api.Members, "toBeOK") {
		t.Error("toBeOK missing")
	}
	if !strings.Contains(api.Origin, "playwright/types/test.d.ts") {
		t.Errorf("origin = %q, want the declaration file", api.Origin)
	}

	loc := surfaceFor(t, got, "LocatorAssertions")
	for _, want := range []string{"toContainText", "toHaveText", "toHaveAttribute", "toHaveScreenshot", "not"} {
		if !memberNamed(loc.Members, want) {
			t.Errorf("LocatorAssertions missing %s; members = %v", want, loc.Members)
		}
	}
}

// Playwright declares two toHaveAttribute overloads; both must survive so the model can see the
// arities that exist.
func TestNodeProvider_keepsOverloads(t *testing.T) {
	p := NewNodeProvider()
	got, _ := p.Lookup(context.Background(), nodeRepo, []Target{{Kind: KindType, Name: "LocatorAssertions"}})
	loc := surfaceFor(t, got, "LocatorAssertions")
	n := 0
	for _, m := range loc.Members {
		if memberName(m) == "toHaveAttribute" {
			n++
		}
	}
	if n != 2 {
		t.Errorf("kept %d toHaveAttribute overloads, want 2: %v", n, loc.Members)
	}
}

// A prefix match would resolve PageAssertions to PageAssertionsToHaveScreenshotOptions, which is
// declared in the same file, and present option fields to the model as assertion methods.
func TestNodeProvider_exactInterfaceNameOnly(t *testing.T) {
	p := NewNodeProvider()
	got, _ := p.Lookup(context.Background(), nodeRepo, []Target{{Kind: KindType, Name: "PageAssertions"}})
	page := surfaceFor(t, got, "PageAssertions")
	if !memberNamed(page.Members, "toHaveURL") {
		t.Errorf("PageAssertions should expose toHaveURL; got %v", page.Members)
	}
	for _, m := range page.Members {
		if memberName(m) == "animations" {
			t.Fatalf("resolved the decoy options interface instead: %v", page.Members)
		}
	}
}

// Nested option objects collapse to {…} so a 92-line screenshot signature costs one line. The
// member NAME and its leading parameters — which is what the model gets wrong — must survive.
func TestNodeProvider_collapsesNestedOptionObjects(t *testing.T) {
	p := NewNodeProvider()
	got, _ := p.Lookup(context.Background(), nodeRepo, []Target{{Kind: KindType, Name: "LocatorAssertions"}})
	loc := surfaceFor(t, got, "LocatorAssertions")

	var screenshot string
	for _, m := range loc.Members {
		if memberName(m) == "toHaveScreenshot" {
			screenshot = m
		}
	}
	if screenshot == "" {
		t.Fatal("toHaveScreenshot not found")
	}
	if strings.Contains(screenshot, "\n") {
		t.Errorf("member should be one line: %q", screenshot)
	}
	// The OUTER options object must collapse too, not just the inner clip:{x,y}. Replacing an
	// inner object with a brace-bearing placeholder stops the collapse one level in, which left
	// `options?: { animations?: …; clip?: {…}; mask?: … }` on the line.
	if !strings.Contains(screenshot, "options?: {…}") {
		t.Errorf("outer options object did not collapse: %q", screenshot)
	}
	if strings.Contains(screenshot, "animations") || strings.Contains(screenshot, "x: number") {
		t.Errorf("option fields leaked through the collapse: %q", screenshot)
	}
	if strings.ContainsRune(screenshot, '\x00') {
		t.Errorf("collapse placeholder leaked into the rendered member: %q", screenshot)
	}
	if !strings.HasPrefix(screenshot, "toHaveScreenshot(name:") {
		t.Errorf("leading parameters lost: %q", screenshot)
	}
}

// Near-miss ranking is what puts the correct member in front of the invented one.
func TestNodeProvider_ranksNearMissFirst(t *testing.T) {
	p := NewNodeProvider()
	got, _ := p.Lookup(context.Background(), nodeRepo, []Target{
		{Kind: KindType, Name: "LocatorAssertions", Member: "toHaveTextContaining"},
	})
	loc := surfaceFor(t, got, "LocatorAssertions")
	if len(loc.Members) == 0 {
		t.Fatal("no members")
	}
	if got := memberName(loc.Members[0]); got != "toHaveText" {
		t.Errorf("first member = %q, want toHaveText ranked first for the near-miss", got)
	}
}

func TestNodeProvider_missesAndErrors(t *testing.T) {
	p := NewNodeProvider()

	t.Run("unknown interface is a miss, not an error", func(t *testing.T) {
		got, err := p.Lookup(context.Background(), nodeRepo, []Target{{Kind: KindType, Name: "NoSuchAssertions"}})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d surfaces, want 0", len(got))
		}
	})

	t.Run("no declarations installed is an actionable error", func(t *testing.T) {
		if _, err := p.Lookup(context.Background(), t.TempDir(), []Target{{Kind: KindType, Name: "LocatorAssertions"}}); err == nil {
			t.Error("expected an error naming the paths that were searched")
		}
	})

	t.Run("empty inputs are a no-op", func(t *testing.T) {
		if got, err := p.Lookup(context.Background(), "", []Target{{Kind: KindType, Name: "X"}}); err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
		if got, err := p.Lookup(context.Background(), nodeRepo, nil); err != nil || got != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
	})

	t.Run("dotted name falls back to its last segment", func(t *testing.T) {
		// A Java- or C#-shaped target reaching this provider should still resolve rather than
		// silently miss.
		got, _ := p.Lookup(context.Background(), nodeRepo, []Target{
			{Kind: KindType, Name: "com.microsoft.playwright.assertions.LocatorAssertions"},
		})
		if len(got) != 1 || got[0].FQCN != "LocatorAssertions" {
			t.Errorf("got %+v, want a LocatorAssertions surface", got)
		}
	})
}

func TestNodeProvider_cacheIsStable(t *testing.T) {
	p := NewNodeProvider()
	targets := []Target{{Kind: KindType, Name: "LocatorAssertions"}}
	first, _ := p.Lookup(context.Background(), nodeRepo, targets)
	for i := 0; i < 5; i++ {
		again, _ := p.Lookup(context.Background(), nodeRepo, targets)
		if len(again) != len(first) || len(again[0].Members) != len(first[0].Members) {
			t.Fatalf("cached lookup differs on call %d", i)
		}
	}
}

func TestNodeProvider_honoursContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := NewNodeProvider()
	if _, err := p.Lookup(ctx, nodeRepo, []Target{{Kind: KindType, Name: "LocatorAssertions"}}); err == nil {
		t.Error("expected the cancelled context to be reported")
	}
}
