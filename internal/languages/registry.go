package languages

import "strings"

const (
	GoLanguage = "go"
	GoVersion  = "1.24"
)

type Definition struct {
	Language       string
	Version        string
	Image          string
	SourceFile     string
	CompileCommand []string
	RunCommand     []string
}

type Registry struct {
	definitions map[string]Definition
}

func NewDefaultRegistry(goImage string) *Registry {
	if strings.TrimSpace(goImage) == "" {
		goImage = "judge-go:1.24"
	}

	return NewRegistry([]Definition{
		{
			Language:       GoLanguage,
			Version:        GoVersion,
			Image:          goImage,
			SourceFile:     "main.go",
			CompileCommand: []string{"sh", "-c", "go build -o /output/program /workspace/main.go"},
			RunCommand:     []string{"sh", "-c", "/output/program"},
		},
	})
}

func NewRegistry(definitions []Definition) *Registry {
	registry := &Registry{
		definitions: make(map[string]Definition, len(definitions)),
	}

	for _, definition := range definitions {
		definition.Language = NormalizeLanguage(definition.Language)
		definition.Version = NormalizeVersion(definition.Version)
		if definition.Language == "" || definition.Version == "" {
			continue
		}

		registry.definitions[key(definition.Language, definition.Version)] = definition
	}

	return registry
}

func (r *Registry) Lookup(language, version string) (Definition, bool) {
	if r == nil {
		return Definition{}, false
	}

	definition, ok := r.definitions[key(NormalizeLanguage(language), NormalizeVersion(version))]
	if !ok {
		return Definition{}, false
	}

	definition.CompileCommand = append([]string(nil), definition.CompileCommand...)
	definition.RunCommand = append([]string(nil), definition.RunCommand...)

	return definition, true
}

func (r *Registry) IsSupported(language, version string) bool {
	_, ok := r.Lookup(language, version)
	return ok
}

func NormalizeLanguage(language string) string {
	return strings.ToLower(strings.TrimSpace(language))
}

func NormalizeVersion(version string) string {
	return strings.TrimSpace(version)
}

func key(language, version string) string {
	return language + "\x00" + version
}
