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
		{"[7.0,4.5]", "[7, 4.5]", true},
		{`{"b":2,"a":1}`, `{"a":1.0,"b":2e0}`, true},
		{"9007199254740992", "9007199254740993", false},
		{"1e3", "1000.00", true},
		{"-0", "0.0", true},
		{"10e999999999", "1e1000000000", true},
		{"1e999999999999999999999999", "1", false},
		{"[1,2]", "[2,1]", false},
		{`{"x":null}`, `{"y":null}`, false},
		{"1", `"1"`, false},
	} {
		if got := outputsMatch(tc.actual, tc.expected); got != tc.want {
			t.Errorf("outputsMatch(%q, %q) = %v, want %v", tc.actual, tc.expected, got, tc.want)
		}
	}
}
