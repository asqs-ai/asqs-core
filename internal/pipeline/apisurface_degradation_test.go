package pipeline

import "testing"
import "github.com/asqs/asqs-core/internal/evaluator/apisurface"

// The acceptance criterion: every unsupported language degrades to NO block, never a wrong or
// empty one. A typed nil would defeat the nil checks the fixer and generator both make.
func TestAPISurfaceProvider_degradesToNoProviderNotAWrongOne(t *testing.T) {
	for _, lang := range []string{"go", "python", "ruby", "", "rust"} {
		if p := apisurface.NewProviderForLang(lang); p != nil {
			t.Errorf("lang %q got provider %T; unsupported languages must yield an untyped nil", lang, p)
		}
	}
	for _, lang := range []string{"java", "csharp", "typescript"} {
		if p := apisurface.NewProviderForLang(lang); p == nil {
			t.Errorf("lang %q must have a provider", lang)
		}
	}
}
