package identity

import (
	"strings"
	"unicode/utf8"
)

func ValidUsername(value string) bool {
	return ValidName(value, 64)
}

func ValidName(value string, max int) bool {
	n := utf8.RuneCountInString(value)
	return n > 0 && n <= max && !strings.ContainsRune(value, 0)
}

func ValidPassword(value string) bool {
	return len(value) > 0 && len(value) <= 72
}
