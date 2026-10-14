//go:build integration

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"code-fight-judge/internal/jobs"
	"code-fight-judge/internal/languages"
	"code-fight-judge/internal/sandbox"
)

// These fixtures are trusted test programs. User submissions are only ever
// executed by the sandbox; the local path verifies adapters without Docker.
func requireLocalRuntimes(t *testing.T) {
	t.Helper()
	if os.Getenv("JUDGE_LOCAL_RUNTIME_TESTS") != "1" {
		t.Skip("set JUDGE_LOCAL_RUNTIME_TESTS=1 for compiler/adapter integration tests")
	}
}

func TestLocalCPPFunctionPrograms(t *testing.T) {
	requireLocalRuntimes(t)
	include := os.Getenv("JUDGE_TEST_CPP_INCLUDE")
	if include == "" {
		include = "/usr/include"
	}
	if _, err := os.Stat(filepath.Join(include, "nlohmann", "json.hpp")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source string
		tests        []struct{ input, want, errorText string }
	}{
		{"typed", `long long Solve(std::vector<long long>& values, int extra) { return values.at(0) + extra; }`, []struct{ input, want, errorText string }{{"[3,4]\n5", "8", ""}, {"[9007199254740993]\n7", "9007199254741000", ""}, {"[3]", "", "invalid input"}, {"[3]\n1\n4", "", "unexpected input"}}},
		{"raw string", `bool solve(const std::string& input) { return input == "aba"; }`, []struct{ input, want, errorText string }{{"aba\n", "true", ""}, {`"aba"`, "true", ""}}},
		{"float result", `std::vector<double> Solve(std::vector<double> values) { return values; }`, []struct{ input, want, errorText string }{{"[7,4.5,0]", "[7,4.5,0]", ""}}},
		{"nested arrays", `std::vector<std::vector<int>> Solve(std::vector<std::vector<int>> values) { return values; }`, []struct{ input, want, errorText string }{{"[[1,2],[3]]", "[[1,2],[3]]", ""}}},
		{"range", `int Solve(int value) { return value; }`, []struct{ input, want, errorText string }{{"2147483648", "", "out of range"}, {"true", "", "expected integer"}}},
		{"own main", `#include <iostream>
int main() { std::cout << "own program"; }`, []struct{ input, want, errorText string }{{"anything", "own program", ""}}},
		{"comment and raw literal", `// int main() {}
int Solve(int value) { std::string text = R"demo(" main() Solve())demo"; return value; }`, []struct{ input, want, errorText string }{{"3", "3", ""}}},
		{"zero args", `int Solve() { return 42; }`, []struct{ input, want, errorText string }{{"", "42", ""}, {"3", "", "unexpected input"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := writeCPPSource(workspace, tc.source); err != nil {
				t.Fatal(err)
			}
			program := filepath.Join(workspace, "program")
			ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
			defer stop()
			command := exec.CommandContext(ctx, "g++", "-std=c++20", "-O2", "-I", include, filepath.Join(workspace, "main.cpp"), "-o", program)
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("compile: %v\n%s", err, output)
			}
			for _, check := range tc.tests {
				checkLocalProgram(t, ctx, exec.CommandContext(ctx, program), check.input, check.want, check.errorText)
			}
		})
	}
}

func TestLocalNodeFunctionPrograms(t *testing.T) {
	requireLocalRuntimes(t)
	root, err := filepath.Abs(filepath.Join("..", "..", "docker", "judge-node"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, extension, source, input, want, errorText, compileError string }{
		{"JS arguments", "js", `function Solve(values, extra) { return values[0]+extra; }`, "[3,4]\n5", "8", "", ""},
		{"JS pretty JSON", "js", `function Solve(values, extra) { return [values, extra]; }`, "[\n 3,\n 4\n]\n5", "[[3,4],5]", "", ""},
		{"JS legacy raw", "js", `function solve(input) { return input.toUpperCase(); } module.exports={solve};`, "a\nb", "A\nB", "", ""},
		{"JS async", "js", `async function Solve(value) { return value+1; }`, "4", "5", "", ""},
		{"JS JSDoc string", "js", `/** @param {string} input */
function Solve(input) { return input === "false"; }`, "false", "true", "", ""},
		{"JS export", "js", `export function Solve(value) { return value; }`, "[1,2]", "[1,2]", "", ""},
		{"JS malformed", "js", `function Solve(value) { return value; }`, "[1,", "", "unterminated JSON", ""},
		{"JS unsafe integer", "js", `function Solve(value) { return value; }`, "9007199254740993", "", "safe number range", ""},
		{"JS missing argument", "js", `function Solve(a,b) { return a+b; }`, "3", "", "expected 2", ""},
		{"JS extra argument", "js", `function Solve(a) { return a; }`, "3\n4", "", "expected 1", ""},
		{"JS undefined", "js", `function Solve(a) {}`, "3", "", "must return", ""},
		{"TS typed", "ts", `export function Solve(values: number[], extra: number): number { return values[0]+extra; }`, "[3,4]\n5", "8", "", ""},
		{"TS raw boolean", "ts", `function Solve(input: string): boolean { return input === "aba"; }`, "aba\n", "true", "", ""},
		{"TS string alias", "ts", `type Input = string; function Solve(input: Input): boolean { return input === "false"; }`, "false", "true", "", ""},
		{"TS arrow", "ts", `const Solve = (values: number[][]): number[][] => values;`, "[[1,2],[3]]", "[[1,2],[3]]", "", ""},
		{"TS type error", "ts", `function Solve(value: number): number { return "wrong"; }`, "1", "", "", "not assignable"},
		{"JS syntax error", "js", `function Solve( {`, "", "", "", "error TS"},
		{"missing entry", "js", `function other(value) { return value; }`, "1", "", "", "top-level function"},
		{"optional", "ts", `function Solve(value?: number): number { return value ?? 0; }`, "1", "", "", "must be required"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			source := filepath.Join(workspace, "solution."+tc.extension)
			output := filepath.Join(workspace, "output", "solution.cjs")
			if err := os.Mkdir(filepath.Dir(output), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte(tc.source), 0644); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			command := exec.CommandContext(ctx, "node", filepath.Join(root, "compile.cjs"), source, output)
			command.Env = append(os.Environ(), "NODE_PATH="+os.Getenv("JUDGE_TEST_NODE_MODULES"))
			compiled, err := command.CombinedOutput()
			if tc.compileError != "" {
				if err == nil || !strings.Contains(string(compiled), tc.compileError) {
					t.Fatalf("compile=%v output=%s", err, compiled)
				}
				return
			}
			if err != nil {
				t.Fatalf("compile=%v output=%s", err, compiled)
			}
			checkLocalProgram(t, ctx, exec.CommandContext(ctx, "node", filepath.Join(root, "run.cjs"), output), tc.input, tc.want, tc.errorText)
		})
	}
}

func checkLocalProgram(t *testing.T, ctx context.Context, command *exec.Cmd, input, want, errorText string) {
	t.Helper()
	command.Stdin = strings.NewReader(input)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if errorText != "" {
		if err == nil || !strings.Contains(stderr.String(), errorText) {
			t.Fatalf("run=%v stderr=%s want=%s", err, &stderr, errorText)
		}
		return
	}
	if err != nil || !outputsMatch(string(output), want) {
		t.Fatalf("output=%q want=%q error=%v stderr=%s", output, want, err, &stderr)
	}
}

func TestRunnerIntegrationNewLanguages(t *testing.T) {
	for _, tc := range []struct{ language, version, source string }{
		{languages.CPPLanguage, languages.CPPVersion, `int Solve(std::vector<int> values, int extra) { return values[0]+extra; }`},
		{languages.JavaScriptLanguage, languages.JavaScriptVersion, `function Solve(values, extra) { return values[0]+extra; }`},
		{languages.TypeScriptLanguage, languages.TypeScriptVersion, `function Solve(values: number[], extra: number): number { return values[0]+extra; }`},
	} {
		t.Run(tc.language, func(t *testing.T) { integrationNewLanguage(t, tc.language, tc.version, tc.source) })
	}
}

func integrationNewLanguage(t *testing.T, language, version, source string) {
	t.Helper()
	cfg, registry := realSandboxIntegrationConfig(t)
	service := NewService(cfg, sandbox.NewRunscExecutor(), registry)
	result, err := service.Run(context.Background(), &jobs.Job{
		ID: "new-language-" + language, Language: language, LanguageVersion: version, SourceCode: source,
		Limits: jobs.Limits{TimeMS: 2000, MemoryMB: 256, OutputKB: 64},
		Tests:  []jobs.TestCase{{ID: "one", Input: "[3,4]\n5", ExpectedOutput: "8", Points: 1}, {ID: "two", Input: "[6]\n7", ExpectedOutput: "13", Points: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "accepted" || result.PassedTests != 2 || result.Score != 3 {
		t.Fatalf("result=%+v", result)
	}
	t.Logf("%s compile=%dms run=%dms peak=%dKiB", language, result.CompileTimeMS, result.RunTimeMS, result.PeakMemoryKB)
}

func TestRunnerIntegrationNewLanguageLimits(t *testing.T) {
	cfg, registry := realSandboxIntegrationConfig(t)
	service := NewService(cfg, sandbox.NewRunscExecutor(), registry)
	for _, runtime := range []struct{ language, version, loop, flood, invalid, fakeExit string }{
		{languages.CPPLanguage, languages.CPPVersion, `int Solve(int x) { for (;;) {} }`, `int Solve(int x) { for (;;) std::cout << "0123456789"; }`, `int Solve(int x) { return; }`, `int Solve(int x) { std::exit(124); }`},
		{languages.JavaScriptLanguage, languages.JavaScriptVersion, `function Solve(x) { for (;;) {} }`, `function Solve(x) { for (;;) process.stdout.write("0123456789"); }`, `function Solve( {`, `function Solve(x) { process.exit(124); }`},
		{languages.TypeScriptLanguage, languages.TypeScriptVersion, `function Solve(x: number): number { for (;;) {} }`, `function Solve(x: number): number { for (;;) process.stdout.write("0123456789"); }`, `function Solve(x: number): number { return "wrong"; }`, `function Solve(x: number): number { process.exit(124); }`},
	} {
		for _, scenario := range []struct {
			name, source, verdict string
			timeout               int
		}{
			{"timeout", runtime.loop, "time_limit_exceeded", 100},
			{"output", runtime.flood, "output_limit_exceeded", 2000},
			{"compile", runtime.invalid, "compile_error", 2000},
			{"exit124", runtime.fakeExit, "runtime_error", 2000},
		} {
			t.Run(runtime.language+"_"+scenario.name, func(t *testing.T) {
				result, err := service.Run(context.Background(), &jobs.Job{
					ID:       "limit-" + runtime.language + "-" + scenario.name,
					Language: runtime.language, LanguageVersion: runtime.version, SourceCode: scenario.source,
					Limits: jobs.Limits{TimeMS: scenario.timeout, MemoryMB: 256, OutputKB: 1},
					Tests:  []jobs.TestCase{{ID: "one", Input: "1", ExpectedOutput: "1", Points: 1}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if result.Verdict != scenario.verdict {
					t.Fatalf("result=%+v want=%s", result, scenario.verdict)
				}
				if len(result.Stdout)+len(result.Stderr) > 1024 && scenario.name == "output" {
					t.Fatal("output exceeded capture budget")
				}
				if scenario.name == "compile" && result.CompileLog == "" {
					t.Fatal("missing compiler diagnostics")
				}
			})
		}
	}
}
