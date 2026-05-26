package languages

import "testing"

func TestRegistryLookupNormalizesLanguageAndVersion(t *testing.T) {
	registry := NewDefaultRegistry(RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})

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

func TestRegistryLookupSupportsPythonWithoutCompileCommand(t *testing.T) {
	registry := NewDefaultRegistry(RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})

	definition, ok := registry.Lookup("python", "3.12")
	if !ok {
		t.Fatal("Lookup() did not find python 3.12")
	}

	if definition.Image != "judge-python:test" {
		t.Fatalf("Lookup().Image = %q, want %q", definition.Image, "judge-python:test")
	}
	if definition.SourceFile != "main.py" {
		t.Fatalf("Lookup().SourceFile = %q, want %q", definition.SourceFile, "main.py")
	}
	if len(definition.CompileCommand) != 0 {
		t.Fatalf("Lookup().CompileCommand len = %d, want 0", len(definition.CompileCommand))
	}
	if len(definition.RunCommand) == 0 {
		t.Fatal("Lookup().RunCommand is empty")
	}
}

func TestRegistrySupportedReturnsPublicLanguageList(t *testing.T) {
	registry := NewDefaultRegistry(RuntimeImages{
		Go:     "judge-go:test",
		Python: "judge-python:test",
	})

	supported := registry.Supported()
	if len(supported) != 2 {
		t.Fatalf("Supported() len = %d, want 2", len(supported))
	}

	if supported[0].Language != GoLanguage {
		t.Fatalf("Supported()[0].Language = %q, want %q", supported[0].Language, GoLanguage)
	}
	if supported[0].Version != GoVersion {
		t.Fatalf("Supported()[0].Version = %q, want %q", supported[0].Version, GoVersion)
	}
	if supported[1].Language != PythonLanguage {
		t.Fatalf("Supported()[1].Language = %q, want %q", supported[1].Language, PythonLanguage)
	}
	if supported[1].Version != PythonVersion {
		t.Fatalf("Supported()[1].Version = %q, want %q", supported[1].Version, PythonVersion)
	}
}

func TestRegistryIsSupported(t *testing.T) {
	registry := NewDefaultRegistry(RuntimeImages{})

	if !registry.IsSupported(" Go ", " 1.24 ") {
		t.Fatal("IsSupported(go 1.24) = false, want true")
	}
	if registry.IsSupported("ruby", "3.3") {
		t.Fatal("IsSupported(ruby 3.3) = true, want false")
	}
	if (*Registry)(nil).IsSupported("go", "1.24") {
		t.Fatal("nil Registry IsSupported() = true, want false")
	}
}

func TestNewRegistrySkipsEmptyDefinitions(t *testing.T) {
	registry := NewRegistry([]Definition{
		{Language: " ", Version: "1"},
		{Language: "go", Version: " "},
		{Language: "go", Version: "1.24"},
	})

	if len(registry.Supported()) != 1 {
		t.Fatalf("Supported() len = %d, want 1", len(registry.Supported()))
	}
}
