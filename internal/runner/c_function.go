package runner

import (
	_ "embed"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed harness/c_main.c
var cFunctionMain string

var (
	cMainPattern  = regexp.MustCompile(`\bmain\s*\(`)
	cSolvePattern = regexp.MustCompile(`\bSolve\s*\(`)
	cLowerPattern = regexp.MustCompile(`\bsolve\s*\(`)
)

func writeCSource(workspace, source string) error {
	code := cppCodeOnly(source)

	if cMainPattern.MatchString(code) {
		return os.WriteFile(
			filepath.Join(workspace, "main.c"),
			[]byte(source),
			0644,
		)
	}

	if err := os.WriteFile(
		filepath.Join(workspace, "solution.c"),
		[]byte(source),
		0644,
	); err != nil {
		return err
	}

	entry := "Solve"
	if !cSolvePattern.MatchString(code) &&
		cLowerPattern.MatchString(code) {
		entry = "solve"
	}

	harness := strings.ReplaceAll(cFunctionMain, "__ENTRY__", entry)

	return os.WriteFile(
		filepath.Join(workspace, "main.c"),
		[]byte(harness),
		0644,
	)
}
