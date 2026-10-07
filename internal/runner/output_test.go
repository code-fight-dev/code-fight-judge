package runner

import "testing"

func TestOutputsMatch(t *testing.T) {
	for _, tc := range []struct {
		actual, expected string
		want             bool
	}{
		{"8\n", "8", true},
		{"one\n two", "one two", false},
		{"[7,4,4]", "[7, 4, 4]", true},
		{"[[1,2],[3]]", "[[1, 2], [3]]", true},
		{`{"x":[1,2]}`, `{ "x": [1, 2] }`, true},
		{`["ab"]`, `["a b"]`, false},
		{"[7,4,5]", "[7, 4, 4]", false},
		{"[1,2", "[1, 2]", false},
		{"[1,2] extra", "[1, 2]", false},
	} {
		if got := outputsMatch(tc.actual, tc.expected); got != tc.want {
			t.Errorf("outputsMatch(%q, %q) = %v, want %v", tc.actual, tc.expected, got, tc.want)
		}
	}
}
