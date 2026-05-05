package languages

import "testing"

func TestRegistryLookupNormalizesLanguageAndVersion(t *testing.T) {
	registry := NewDefaultRegistry("judge-go:test")

	definition, ok := registry.Lookup(" Go ", " 1.24 ")
	if !ok {
		t.Fatal("Lookup() did not find go 1.24")
	}

	if definition.Language != GoLanguage {
		t.Fatalf("Lookup().Language = %q, want %q", definition.Language, GoLanguage)
	}
	if definition.Version != GoVersion {
		t.Fatalf("Lookup().Version = %q, want %q", definition.Version, GoVersion)
	}
	if definition.Image != "judge-go:test" {
		t.Fatalf("Lookup().Image = %q, want %q", definition.Image, "judge-go:test")
	}
}

func TestRegistrySupportedReturnsPublicLanguageList(t *testing.T) {
	registry := NewDefaultRegistry("judge-go:test")

	supported := registry.Supported()
	if len(supported) != 1 {
		t.Fatalf("Supported() len = %d, want 1", len(supported))
	}

	if supported[0].Language != GoLanguage {
		t.Fatalf("Supported()[0].Language = %q, want %q", supported[0].Language, GoLanguage)
	}
	if supported[0].Version != GoVersion {
		t.Fatalf("Supported()[0].Version = %q, want %q", supported[0].Version, GoVersion)
	}
}
