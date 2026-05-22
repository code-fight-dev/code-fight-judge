package languages

import (
	"slices"
	"strings"
)

const (
	GoLanguage     = "go"
	GoVersion      = "1.24"
	PythonLanguage = "python"
	PythonVersion  = "3.12"
)

type RuntimeImages struct {
	Go     string
	Python string
}

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

type SupportedLanguage struct {
	Language string `json:"language"`
	Version  string `json:"version"`
}

func NewDefaultRegistry(images RuntimeImages) *Registry {
	if strings.TrimSpace(images.Go) == "" {
		images.Go = "judge-go:1.24"
	}
	if strings.TrimSpace(images.Python) == "" {
		images.Python = "judge-python:3.12"
	}

	return NewRegistry([]Definition{
		{
			Language:       GoLanguage,
			Version:        GoVersion,
			Image:          images.Go,
			SourceFile:     "main.go",
			CompileCommand: []string{"sh", "-c", "go build -o /output/program /workspace/*.go"},
			RunCommand:     []string{"sh", "-c", "/output/program"},
		},
		{
			Language:   PythonLanguage,
			Version:    PythonVersion,
			Image:      images.Python,
			SourceFile: "main.py",
			RunCommand: []string{"python3", "-B", "/workspace/main.py"},
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

func (r *Registry) Supported() []SupportedLanguage {
	if r == nil {
		return []SupportedLanguage{}
	}

	supported := make([]SupportedLanguage, 0, len(r.definitions))
	for _, definition := range r.definitions {
		supported = append(supported, SupportedLanguage{
			Language: definition.Language,
			Version:  definition.Version,
		})
	}

	slices.SortFunc(supported, func(left, right SupportedLanguage) int {
		if left.Language != right.Language {
			return strings.Compare(left.Language, right.Language)
		}

		return strings.Compare(left.Version, right.Version)
	})

	return supported
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
