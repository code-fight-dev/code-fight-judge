package runner

import (
	"encoding/json"
	"io"
	"math/big"
	"strconv"
	"strings"
)

func outputsMatch(actual, expected string) bool {
	if normalizeOutput(actual) == normalizeOutput(expected) {
		return true
	}
	actualJSON, actualOK := decodeOutputJSON(actual)
	expectedJSON, expectedOK := decodeOutputJSON(expected)
	return actualOK && expectedOK && equalJSON(actualJSON, expectedJSON)
}

func decodeOutputJSON(output string) (any, bool) {
	decoder := json.NewDecoder(strings.NewReader(output))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	var extra any
	return value, decoder.Decode(&extra) == io.EOF
}

func equalJSON(left, right any) bool {
	switch value := left.(type) {
	case json.Number:
		other, ok := right.(json.Number)
		if !ok {
			return false
		}
		coefficient, exponent, valid := normalizedJSONNumber(value.String())
		otherCoefficient, otherExponent, otherValid := normalizedJSONNumber(other.String())
		return valid && otherValid && coefficient == otherCoefficient && exponent == otherExponent
	case []any:
		other, ok := right.([]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for i := range value {
			if !equalJSON(value[i], other[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		other, ok := right.(map[string]any)
		if !ok || len(value) != len(other) {
			return false
		}
		for key, item := range value {
			candidate, exists := other[key]
			if !exists || !equalJSON(item, candidate) {
				return false
			}
		}
		return true
	default:
		return left == right
	}
}

// Compare decimal numbers exactly, without converting to float64 or expanding
// exponents into huge integers. Submitted output must not control allocations
// through values such as 1e1000000000.
func normalizedJSONNumber(number string) (string, string, bool) {
	parts := strings.FieldsFunc(number, func(r rune) bool { return r == 'e' || r == 'E' })
	mantissa := parts[0]
	negative := strings.HasPrefix(mantissa, "-")
	mantissa = strings.TrimPrefix(mantissa, "-")
	fraction := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		fraction = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	coefficient := strings.TrimLeft(mantissa, "0")
	if coefficient == "" {
		return "0", "0", true
	}
	var parsedExponent int64
	if len(parts) == 2 {
		var err error
		parsedExponent, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return "", "", false
		}
	}
	trimmed := strings.TrimRight(coefficient, "0")
	exponent := big.NewInt(parsedExponent)
	exponent.Add(exponent, big.NewInt(int64(len(coefficient)-len(trimmed)-fraction)))
	if negative {
		trimmed = "-" + trimmed
	}
	return trimmed, exponent.String(), true
}
