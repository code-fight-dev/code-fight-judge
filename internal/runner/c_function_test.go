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
