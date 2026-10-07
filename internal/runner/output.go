package runner

import (
	"bytes"
	"encoding/json"
)

func outputsMatch(actual, expected string) bool {
	if normalizeOutput(actual) == normalizeOutput(expected) {
		return true
	}
	// JSON marshaling omits spaces after commas and colons. Accept the same
	// values with different formatting without ignoring spaces inside strings.
	var actualJSON, expectedJSON bytes.Buffer
	if json.Compact(&actualJSON, []byte(actual)) != nil || json.Compact(&expectedJSON, []byte(expected)) != nil {
		return false
	}
	return bytes.Equal(actualJSON.Bytes(), expectedJSON.Bytes())
}
