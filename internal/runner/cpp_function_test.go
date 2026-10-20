package runner

import (
	"strings"
	"testing"
)

func TestCPPCodeOnlyIgnoresUnterminatedRawLiteral(t *testing.T) {
	for _, source := range []string{`R"( " main() " `, `R"tag( " Solve() " `} {
		code := cppCodeOnly(source)
		if cppMainPattern.MatchString(code) || cppSolvePattern.MatchString(code) {
			t.Fatalf("entry point leaked out of an unterminated raw literal: %q", code)
		}
	}
}

func TestCPPCodeOnlyPreservesEntryAfterRawLiteral(t *testing.T) {
	for _, delimiter := range []string{"", strings.Repeat("x", 16)} {
		source := `auto text = R"` + delimiter + `(main())` + delimiter + `"; int Solve() { return 0; }`
		code := cppCodeOnly(source)
		if cppMainPattern.MatchString(code) || !cppSolvePattern.MatchString(code) {
			t.Fatalf("entry point detection failed with delimiter %q: %q", delimiter, code)
		}
	}
}

func BenchmarkCPPCodeOnlyMalformedRawLiterals(b *testing.B) {
	source := strings.Repeat(`R"`, 256*1024)
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for b.Loop() {
		cppCodeOnly(source)
	}
}
