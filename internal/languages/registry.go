package languages

import (
	"slices"
	"strings"
)

const (
	GoLanguage         = "go"
	GoVersion          = "1.24"
	PythonLanguage     = "python"
	PythonVersion      = "3.12"
	CPPLanguage        = "cpp"
	CPPVersion         = "20"
	JavaScriptLanguage = "javascript"
	JavaScriptVersion  = "node24"
	TypeScriptLanguage = "typescript"
	TypeScriptVersion  = "5.9.3"
)

type RuntimeImages struct {
	Go     string
	Python string
	CPP    string
	Node   string
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
		images.Go = "judge-go:1.27"
	}
	if strings.TrimSpace(images.Python) == "" {
		images.Python = "judge-python:3.12"
	}
	if strings.TrimSpace(images.CPP) == "" {
		images.CPP = "judge-cpp:20"
	}
	if strings.TrimSpace(images.Node) == "" {
		images.Node = "judge-node:24"
	}

	return NewRegistry([]Definition{
		{
			Language: CPPLanguage, Version: CPPVersion, Image: images.CPP,
			SourceFile:     "main.cpp",
			CompileCommand: []string{"g++", "-std=c++20", "-O2", "-pipe", "/workspace/main.cpp", "-o", "/output/program"},
			RunCommand:     []string{"/output/program"},
		},
		{
			Language: JavaScriptLanguage, Version: JavaScriptVersion, Image: images.Node,
			SourceFile:     "solution.js",
			CompileCommand: []string{"node", "/opt/judge-node/compile.cjs", "/workspace/solution.js", "/output/solution.cjs"},
			RunCommand:     []string{"node", "/opt/judge-node/run.cjs", "/output/solution.cjs"},
		},
		{
			Language: TypeScriptLanguage, Version: TypeScriptVersion, Image: images.Node,
			SourceFile:     "solution.ts",
			CompileCommand: []string{"node", "/opt/judge-node/compile.cjs", "/workspace/solution.ts", "/output/solution.cjs"},
			RunCommand:     []string{"node", "/opt/judge-node/run.cjs", "/output/solution.cjs"},
		},
		{
			Language:       GoLanguage,
			Version:        GoVersion,
			Image:          images.Go,
			SourceFile:     "main.go",
			CompileCommand: []string{"sh", "-c", "GOMAXPROCS=1 go build -p 1 -o /output/program /workspace/*.go"},
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
