package uitesthooks

import "testing"

func TestKebabAndSlug(t *testing.T) {
	for in, want := range map[string]string{
		"HomePage":             "home-page",
		"OrdersPage.tsx":       "orders-page-tsx",
		"Welcome to ASQS":      "welcome-to-asqs",
		"  Go to   orders!  ":  "go-to-orders",
		"catalog-search-input": "catalog-search-input",
		"HTMLParser2Fast":      "htmlparser2-fast",
		"":                     "",
		"---":                  "",
		"Continue to the next step of the checkout": "continue-to-the-next-ste",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFilePrefix(t *testing.T) {
	for rel, want := range map[string]string{
		"src/pages/HomePage.tsx":                          "home-page",
		"src/features/orders/index.tsx":                   "orders",
		"src/app/features/catalog/catalog.component.html": "catalog",
		"src/legacy/LegacyBanner.jsx":                     "legacy-banner",
	} {
		if got := FilePrefix(rel); got != want {
			t.Errorf("FilePrefix(%q) = %q, want %q", rel, got, want)
		}
	}
}

// Names are derived, so a reviewer can predict them, and unique within a file, so two identical
// buttons do not collide.
func TestHookNameAndUniqueness(t *testing.T) {
	if got := HookName("home-page", "link", "orders"); got != "home-page-link-orders" {
		t.Fatalf("got %q", got)
	}
	if got := HookName("home-page", "root", ""); got != "home-page-root" {
		t.Fatalf("got %q", got)
	}
	n := nameSet{}
	a, b, c := n.take("x-button"), n.take("x-button"), n.take("x-button")
	if a != "x-button" || b != "x-button-2" || c != "x-button-3" {
		t.Fatalf("got %q %q %q", a, b, c)
	}
}
