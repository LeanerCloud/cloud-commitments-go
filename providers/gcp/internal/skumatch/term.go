package skumatch

import (
	"fmt"
	"strings"
)

// TermYears parses a recommendation term label into commitment years. It is
// case-insensitive, ignores surrounding whitespace, and accepts "1yr", "1",
// "12mo" and "" (an unset term has always meant one year) for one year and
// "3yr", "3", "36mo" for three. Anything else is an error: guessing a term
// would price the wrong commitment.
func TermYears(term string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(term)) {
	case "", "1yr", "1", "12mo":
		return 1, nil
	case "3yr", "3", "36mo":
		return 3, nil
	}
	return 0, fmt.Errorf("unsupported commitment term %q, want 1yr or 3yr", term)
}
