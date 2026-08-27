package apisurface

import "testing"

const runtimeHintsSource = `package org.springframework.samples.petclinic;

import org.springframework.aot.hint.RuntimeHints;
import org.springframework.aot.hint.RuntimeHintsRegistrar;
import org.springframework.samples.petclinic.model.BaseEntity;

public class PetClinicRuntimeHints implements RuntimeHintsRegistrar {
	@Override
	public void registerHints(RuntimeHints hints, ClassLoader classLoader) {
		hints.resources().registerPattern("db/*");
	}
}
`

func names(ts []Target) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name)
	}
	return out
}

func contains(ts []Target, name string) bool {
	for _, n := range names(ts) {
		if n == name {
			return true
		}
	}
	return false
}

func TestSignatureTargets_resolvesParameterTypeThroughImports(t *testing.T) {
	got := SignatureTargets("java", "public void registerHints(RuntimeHints hints, ClassLoader classLoader)", runtimeHintsSource)
	if !contains(got, "org.springframework.aot.hint.RuntimeHints") {
		t.Fatalf("RuntimeHints not resolved through the import block, got %v", names(got))
	}
	for _, tt := range got {
		if tt.Kind != KindType {
			t.Errorf("%s: want KindType (member dump), got %s — a KindSymbol renders only an import line", tt.Name, tt.Kind)
		}
	}
}

// ClassLoader is in the signature but not imported (java.lang), so there is nothing to resolve it
// to. Guessing a package is the failure this whole mechanism exists to prevent.
func TestSignatureTargets_skipsUnimportedNames(t *testing.T) {
	if got := SignatureTargets("java", "public void registerHints(RuntimeHints hints, ClassLoader classLoader)", runtimeHintsSource); contains(got, "java.lang.ClassLoader") {
		t.Errorf("an unimported simple name must not be resolved, got %v", names(got))
	}
}

// Types the file imports but the signature never mentions must not be looked up: import-driven
// targets are what would spend the prompt on 40-member dumps of java.util.List.
func TestSignatureTargets_ignoresImportsAbsentFromSignature(t *testing.T) {
	got := SignatureTargets("java", "public void registerHints(RuntimeHints hints, ClassLoader classLoader)", runtimeHintsSource)
	if contains(got, "org.springframework.aot.hint.RuntimeHintsRegistrar") {
		t.Errorf("RuntimeHintsRegistrar is an import, not a signature type, got %v", names(got))
	}
}

func TestSignatureTargets_dropsJavaLangAndStaticAndWildcardImports(t *testing.T) {
	src := `package a;

import static org.assertj.core.api.Assertions.assertThat;
import java.util.*;
import java.lang.Runnable;

public class A {}
`
	got := SignatureTargets("java", "public void run(Runnable r, Assertions a, List l)", src)
	if len(got) != 0 {
		t.Errorf("want no targets (java.lang, static import, wildcard import), got %v", names(got))
	}
}

func TestSignatureTargets_capped(t *testing.T) {
	src := `package a;

import x.y.Alpha;
import x.y.Bravo;
import x.y.Charlie;
import x.y.Delta;
import x.y.Echo;

public class A {}
`
	got := SignatureTargets("java", "public void go(Alpha a, Bravo b, Charlie c, Delta d, Echo e)", src)
	if len(got) != maxSignatureTargets {
		t.Errorf("want the list capped at %d, got %d (%v)", maxSignatureTargets, len(got), names(got))
	}
}

func TestSignatureTargets_nonJavaAndEmptyInputsAreNoOps(t *testing.T) {
	for _, tc := range []struct{ name, lang, sig, src string }{
		{"csharp", "csharp", "public void Go(RuntimeHints h)", runtimeHintsSource},
		{"typescript", "typescript", "go(h: RuntimeHints)", runtimeHintsSource},
		{"empty signature", "java", "", runtimeHintsSource},
		{"empty source", "java", "public void go(RuntimeHints h)", ""},
		{"source with no imports", "java", "public void go(RuntimeHints h)", "package a;\npublic class A {}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SignatureTargets(tc.lang, tc.sig, tc.src); len(got) != 0 {
				t.Errorf("want no targets, got %v", names(got))
			}
		})
	}
}

// The two lookups that were pure prompt cost in run api-3fdd28e8f16a37247fa6494315ff6176.
func TestSignatureTargets_skipsWellKnownContainers(t *testing.T) {
	src := `package a;

import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.stream.Collectors;
import org.springframework.ui.Model;

public class A {}
`
	got := SignatureTargets("java", "public Map<String,Object> go(List<Vet> vets, Optional<Vet> one, Collectors c, Model model)", src)
	for _, unwanted := range []string{"java.util.List", "java.util.Map", "java.util.Optional", "java.util.stream.Collectors"} {
		if contains(got, unwanted) {
			t.Errorf("%s is a well-known container and must not spend a lookup, got %v", unwanted, names(got))
		}
	}
	if !contains(got, "org.springframework.ui.Model") {
		t.Errorf("the type actually worth resolving was dropped, got %v", names(got))
	}
}

// The container skip is signature-scoped. The fixer's classifier must keep letting these through:
// a diagnostic naming a Map overload is pointing at the member the model got wrong.
func TestWellKnownContainerSkipDoesNotLeakIntoTheFixerFilter(t *testing.T) {
	for _, fqcn := range []string{"java.util.List", "java.util.Map", "java.util.regex.Pattern"} {
		if IsUninterestingType(fqcn) {
			t.Errorf("%s must stay eligible for diagnostic-driven lookups; only SignatureTargets skips containers", fqcn)
		}
	}
	// …and java.util.regex.Pattern is not a container, so even the signature path keeps it.
	if isWellKnownContainerType("java.util.regex.Pattern") {
		t.Error("Pattern is the documented counter-example: its member dump answers `new Pattern(...)`")
	}
}

// A skipped container must not consume one of the maxSignatureTargets slots. In run
// api-3fdd28e8f16a37247fa6494315ff6176, VisitController#loadPetWithVisit spent a slot on
// java.util.Map alongside PathVariable; the slot should go to a type worth resolving instead.
func TestSignatureTargets_skippedContainersDoNotConsumeCapSlots(t *testing.T) {
	src := `package a;

import java.util.List;
import java.util.Map;
import java.util.Set;
import x.y.Alpha;
import x.y.Bravo;
import x.y.Charlie;

public class A {}
`
	got := SignatureTargets("java", "public void go(List l, Map m, Set s, Alpha a, Bravo b, Charlie c)", src)
	if len(got) != maxSignatureTargets {
		t.Fatalf("want %d real targets, got %d (%v)", maxSignatureTargets, len(got), names(got))
	}
	for _, want := range []string{"x.y.Alpha", "x.y.Bravo", "x.y.Charlie"} {
		if !contains(got, want) {
			t.Errorf("%s was crowded out by a skipped container, got %v", want, names(got))
		}
	}
}
