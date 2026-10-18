package runner

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed harness/cpp_main.cpp
var cppFunctionMain string

var cppMainPattern = regexp.MustCompile(`\bmain\s*\(`)
var cppSolvePattern = regexp.MustCompile(`\bSolve\s*\(`)
var cppLegacySolvePattern = regexp.MustCompile(`\bsolve\s*\(`)

func writeCPPSource(workspace, source string) error {
	code := cppCodeOnly(source)
	if cppMainPattern.MatchString(code) {
		return os.WriteFile(filepath.Join(workspace, "main.cpp"), []byte(source), 0644)
	}
	if err := os.WriteFile(filepath.Join(workspace, "solution.cpp"), []byte(source), 0644); err != nil {
		return err
	}
	entry := "solve"
	if cppSolvePattern.MatchString(code) || !cppLegacySolvePattern.MatchString(code) {
		entry = "Solve"
	}
	return os.WriteFile(filepath.Join(workspace, "main.cpp"), []byte(strings.ReplaceAll(cppFunctionMain, "__ENTRY__", entry)), 0644)
}

// Ignore comments and literals when detecting entry points, including C++ raw
// strings. Invalid source is still diagnosed by the compiler in the sandbox.
func cppCodeOnly(source string) string {
	var code strings.Builder
	for i := 0; i < len(source); {
		end := i
		switch {
		case strings.HasPrefix(source[i:], "//"):
			end = i + 2
			for end < len(source) && source[end] != '\n' {
				end++
			}
		case strings.HasPrefix(source[i:], "/*"):
			end = len(source)
			if offset := strings.Index(source[i+2:], "*/"); offset >= 0 {
				end = i + 2 + offset + 2
			}
		case strings.HasPrefix(source[i:], `R"`):
			if open := strings.IndexByte(source[i+2:], '('); open >= 0 && open <= 16 {
				delimiter := source[i+2 : i+2+open]
				start := i + 3 + open
				if close := strings.Index(source[start:], ")"+delimiter+`"`); close >= 0 {
					end = start + close + len(delimiter) + 2
				}
			}
		case source[i] >= '0' && source[i] <= '9':
			end = i + 1
			for end < len(source) {
				c := source[end]
				if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' || c == '.' || c == '\'') {
					break
				}
				end++
			}
			// Keep names such as main2 distinct from the main entry point.
			code.WriteByte('0')
		case source[i] == '"' || source[i] == '\'':
			quote := source[i]
			end = i + 1
			for end < len(source) {
				if source[end] == '\\' {
					end += 2
					continue
				}
				end++
				if source[end-1] == quote {
					break
				}
			}
		}
		if end > i {
			code.WriteByte(' ')
			i = min(end, len(source))
			continue
		}
		code.WriteByte(source[i])
		i++
	}
	return code.String()
}
