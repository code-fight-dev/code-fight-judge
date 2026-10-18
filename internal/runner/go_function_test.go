package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/config"
	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
)

const pricingFunction = `func maxProfit(prices []int) int {
	if len(prices) == 0 { return 0 }
	minimum, best := prices[0], 0
	for _, price := range prices[1:] {
		if gain := price - minimum; gain > best { best = gain }
		if price < minimum { minimum = price }
	}
	return best
}`

// Compile and execute the generated files rather than only inspecting text.
func TestGoFunctionPrograms(t *testing.T) {
	cases := []struct {
		name, source, input, want, stderr string
	}{
		{"pricing gain", pricingFunction, "[12, 7, 10, 5, 13]", "8", ""},
		{"pricing decline", pricingFunction, "[9, 8, 6]", "0", ""},
		{"single day", pricingFunction, "[42]", "0", ""},
		{"zero price", pricingFunction, "[0, 1000000]", "1000000", ""},
		{"equal prices", pricingFunction, "[7, 7]", "0", ""},
		{"typed Solve", strings.Replace(pricingFunction, "maxProfit", "Solve", 1), "[5, 13]", "8", ""},
		{"typed solve", strings.Replace(pricingFunction, "maxProfit", "solve", 1), "[5, 13]", "8", ""},
		{"multiple arguments", `func Solve(values []int, limit int) int { return values[0] + limit }`, "[3, 4]\n5", "8", ""},
		{"grouped arguments", `func Solve(a, b int) int { return a + b }`, "3\n5", "8", ""},
		{"helper function", `func helper(x int) int { return x * 2 }; func Solve(x int) int { return helper(x) }`, "4", "8", ""},
		{"named array type", `type Prices []int; func Solve(prices Prices) int { return prices[0] }`, "[8]", "8", ""},
		{"raw text", `func palindrome(log string) bool { return log == "aba" }`, "aba\n", "true", ""},
		{"JSON text", `func palindrome(log string) bool { return log == "aba" }`, `"aba"`, "true", ""},
		{"typed string result", `func Solve(x int) string { return "answer" }`, "8", "answer", ""},
		{"nested array result", `func Solve(values [][]int) [][]int { return values }`, "[[1, 2], [3]]", "[[1,2],[3]]", ""},
		{"alias imports", `import j "encoding/json"; func Solve(x int) int { var n int; j.Unmarshal([]byte("8"), &n); return n }`, "0", "8", ""},
		{"package name entry", `func json(x int) int { return x }`, "8", "8", ""},
		{"harness alias entry", `func _cfFmt(x int) int { return x }`, "8", "8", ""},
		{"legacy string API", `func Solve(input string) string { return input }`, "[1, 2]\n", "[1, 2]\n", ""},
		{"own main", `package main; import "fmt"; func main() { fmt.Print("own program") }`, "anything", "own program", ""},
		{"commented main", "// func main() {}\n" + pricingFunction, "[5, 13]", "8", ""},
		{"invalid argument", pricingFunction, "not an array", "", "invalid input for argument 1: expected []int"},
		{"missing argument", `func Solve(a, b int) int { return a + b }`, "3", "", "invalid input for argument 2"},
		{"extra argument", pricingFunction, "[5, 13]\n99", "", "unexpected input after the last function argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			language := languages.Definition{Language: languages.GoLanguage, SourceFile: "main.go"}
			if err := writeSourceFiles(workspace, language, tc.source); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(workspace, "*.go"))
			if err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(workspace, "program")
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			args := append([]string{"build", "-o", program}, files...)
			cmd := exec.CommandContext(ctx, "go", args...)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("compile: %v\n%s", err, output)
			}
			cmd = exec.CommandContext(ctx, program)
			cmd.Stdin = strings.NewReader(tc.input)
			var stderr strings.Builder
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if tc.stderr != "" {
				if err == nil || !strings.Contains(stderr.String(), tc.stderr) {
					t.Fatalf("error=%v stderr=%q, want %q", err, stderr.String(), tc.stderr)
				}
				return
			}
			if err != nil || string(output) != tc.want {
				t.Fatalf("output=%q error=%v stderr=%q, want %q", output, err, stderr.String(), tc.want)
			}
		})
	}
}

func TestInvalidGoFunctionIsCompileError(t *testing.T) {
	for _, source := range []string{
		`func one(x int) int { return x }; func two(x int) int { return x }`,
		`func Solve(x int) {}`,
		`func Solve(x int) (int, int) { return x, x }`,
		`func Solve[T any](x T) T { return x }`,
		`func Solve(x ...int) int { return 0 }`,
	} {
		executor := &fakeExecutor{}
		service := NewService(config.Config{JudgeWorkDir: t.TempDir()}, executor,
			languages.NewDefaultRegistry(languages.RuntimeImages{}))
		result, err := service.Run(context.Background(), &jobs.Job{
			Language: "go", LanguageVersion: "1.24", SourceCode: source,
			Tests: []jobs.TestCase{{Input: "4", ExpectedOutput: "4"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Verdict != "compile_error" || result.Stderr == "" || result.TotalTests != 1 {
			t.Fatalf("unexpected result: %+v", result)
		}
		if len(executor.requests) != 0 {
			t.Fatal("invalid entry point invoked sandbox")
		}
	}
}

func TestGoSyntaxErrorsAreLeftToCompiler(t *testing.T) {
	workspace := t.TempDir()
	if err := writeSourceFiles(workspace, languages.Definition{Language: "go", SourceFile: "main.go"}, "func Solve("); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, "main.go")); err != nil {
		t.Fatal(err)
	}
}
