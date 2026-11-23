package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteCSourceWithMain(t *testing.T) {
	workspace := t.TempDir()

	source := `int main(void) { return 0; }`

	if err := writeCSource(workspace, source); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(workspace, "main.c"))
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != source {
		t.Fatal("main.c does not contain original source")
	}
}

func TestWriteCSourceWithSolve(t *testing.T) {
	for _, entry := range []string{"Solve", "solve"} {
		t.Run(entry, func(t *testing.T) {
			workspace := t.TempDir()

			source := "const char *" + entry +
				"(const char *input) { return input; }"

			if err := writeCSource(workspace, source); err != nil {
				t.Fatal(err)
			}

			solution, err := os.ReadFile(
				filepath.Join(workspace, "solution.c"),
			)
			if err != nil {
				t.Fatal(err)
			}

			if string(solution) != source {
				t.Fatal("solution.c does not match source")
			}

			main, err := os.ReadFile(
				filepath.Join(workspace, "main.c"),
			)
			if err != nil {
				t.Fatal(err)
			}

			if !strings.Contains(
				string(main),
				"#define CF_ENTRY "+entry,
			) {
				t.Fatalf("incorrect function entry: %s", main)
			}
		})
	}
}

func TestWriteCSourceIgnoresCommentedMain(t *testing.T) {
	workspace := t.TempDir()

	source := `
// int main(void) { return 0; }
const char *Solve(const char *input) {
    return input;
}
`

	if err := writeCSource(workspace, source); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(
		filepath.Join(workspace, "solution.c"),
	); err != nil {
		t.Fatal("expected solution.c to be created:", err)
	}
}

func TestWriteCSourceWithTypedFunction(t *testing.T) {
	workspace := t.TempDir()

	source := `int Solve(int a, int b) {
		return a + b;
	}`

	if err := writeCSource(workspace, source); err != nil {
		t.Fatal(err)
	}

	header, err := os.ReadFile(
		filepath.Join(workspace, "c_typed.h"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(
		string(header),
		"cf_judge_run_int2",
	) {
		t.Fatal("typed function handler is missing")
	}

	main, err := os.ReadFile(
		filepath.Join(workspace, "main.c"),
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(
		string(main),
		`#include "c_typed.h"`,
	) {
		t.Fatal("typed function header is not included")
	}
}
