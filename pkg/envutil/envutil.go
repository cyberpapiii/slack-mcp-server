package envutil

import "strings"

// IsTruthy reports whether value is a boolean-style env enablement
// (true/1/yes), case-insensitive, ignoring surrounding whitespace.
func IsTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes":
		return true
	default:
		return false
	}
}

// IsFalsy reports whether value explicitly turns something off
// (false/0/no/off), case-insensitive, ignoring surrounding whitespace.
func IsFalsy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "false", "0", "no", "off":
		return true
	default:
		return false
	}
}
