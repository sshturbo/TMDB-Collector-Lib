package normalization

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Title converts a title to a comparison form while retaining its letters and numbers.
// Accents are folded, punctuation becomes spacing, and repeated whitespace is collapsed.
func Title(value string) string {
	value = norm.NFD.String(strings.ToLower(strings.TrimSpace(value)))
	var result strings.Builder
	space := true
	for _, r := range value {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			result.WriteRune(r)
			space = false
			continue
		}
		if !space && result.Len() > 0 {
			result.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(result.String())
}
