package runner

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strings"
)

// goSubmissionError distinguishes an invalid user entry point from an I/O failure.
type goSubmissionError struct{ message string }

func (e *goSubmissionError) Error() string { return e.message }

// buildGoFunctionHarness returns an empty harness for the legacy string API.
// Invalid Go syntax is left to the compiler for its normal source diagnostics.
func buildGoFunctionHarness(sourceCode string) (string, error) {
	file, err := parser.ParseFile(token.NewFileSet(), "solution.go", sourceCode, 0)
	if err != nil {
		return "", nil
	}

	var functions []*ast.FuncDecl
	var entry *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name == "init" {
			continue
		}
		functions = append(functions, fn)
		if fn.Name.Name == "Solve" {
			entry = fn
		}
	}
	if entry == nil {
		for _, fn := range functions {
			if fn.Name.Name == "solve" {
				entry = fn
				break
			}
		}
	}
	if entry == nil && len(functions) == 1 {
		entry = functions[0]
	}
	if entry == nil {
		return "", &goSubmissionError{"Go entry point is ambiguous or missing: name your solution Solve or solve, submit a single function, or provide func main()"}
	}
	if entry.Type.TypeParams != nil || entry.Type.Results == nil || entry.Type.Results.NumFields() != 1 {
		return "", &goSubmissionError{"Go solution functions must be non-generic and return exactly one value; provide func main() for other signatures"}
	}
	for _, field := range entry.Type.Params.List {
		if _, variadic := field.Type.(*ast.Ellipsis); variadic {
			return "", &goSubmissionError{"Go solution functions cannot be variadic; use a slice parameter or provide func main()"}
		}
	}
	if entry.Name.Name == "Solve" && entry.Type.Params.NumFields() == 1 &&
		isGoString(entry.Type.Params.List[0].Type) && isGoString(entry.Type.Results.List[0].Type) {
		return "", nil
	}

	prefix := "_cf"
	for name := range file.Scope.Objects {
		for strings.HasPrefix(name, prefix) {
			prefix += "_"
		}
	}
	harness := strings.ReplaceAll(goTypedFunctionMain, "_cf", prefix)
	harness = strings.ReplaceAll(harness, "__ENTRY__", entry.Name.Name)
	formatted, err := format.Source([]byte(harness))
	if err != nil {
		return "", fmt.Errorf("format Go function harness: %w", err)
	}
	return string(formatted), nil
}

func isGoString(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "string"
}

// Keep harness imports in a separate file so user import aliases cannot collide.
const goTypedFunctionMain = `package main

import (
	_cfBytes "bytes"
	_cfJSON "encoding/json"
	_cfFmt "fmt"
	_cfIO "io"
	_cfOS "os"
	_cfReflect "reflect"
	_cfStrings "strings"
)

func main() {
	input, err := _cfIO.ReadAll(_cfOS.Stdin)
	if err != nil { panic(err) }
	function := _cfReflect.ValueOf(__ENTRY__)
	signature := function.Type()
	arguments := make([]_cfReflect.Value, signature.NumIn())
	decoder := _cfJSON.NewDecoder(_cfBytes.NewReader(input))
	for i := range arguments {
		value := _cfReflect.New(signature.In(i))
		if signature.NumIn() == 1 && signature.In(i).Kind() == _cfReflect.String {
			// Existing text tasks use raw stdin; also accept JSON string literals.
			var text string
			if err := _cfJSON.Unmarshal(input, &text); err != nil {
				text = _cfStrings.TrimRight(string(input), "\r\n")
			}
			value.Elem().SetString(text)
		} else if err := decoder.Decode(value.Interface()); err != nil {
			_cfFmt.Fprintf(_cfOS.Stderr, "invalid input for argument %d: expected %s: %v\n", i+1, signature.In(i), err)
			_cfOS.Exit(1)
		}
		arguments[i] = value.Elem()
	}
	if !(signature.NumIn() == 1 && signature.In(0).Kind() == _cfReflect.String) {
		var extra _cfJSON.RawMessage
		if err := decoder.Decode(&extra); err != _cfIO.EOF {
			_cfFmt.Fprintln(_cfOS.Stderr, "unexpected input after the last function argument")
			_cfOS.Exit(1)
		}
	}
	result := function.Call(arguments)[0]
	if result.Kind() == _cfReflect.String {
		_cfFmt.Print(result.String())
		return
	}
	output, err := _cfJSON.Marshal(result.Interface())
	if err != nil { panic(err) }
	_cfFmt.Print(string(output))
}
`
