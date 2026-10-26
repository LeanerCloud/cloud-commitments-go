package skumatch

import "fmt"

// TermYears parses a recommendation term label into commitment years. It
// accepts "1yr", "1" and "" (an unset term has always meant one year) for one
// year and "3yr", "3" for three. Anything else is an error: guessing a term
// would price the wrong commitment.
func TermYears(term string) (int, error) {
	switch term {
	case "", "1yr", "1":
		return 1, nil
	case "3yr", "3":
		return 3, nil
	}
	return 0, fmt.Errorf("unsupported commitment term %q, want 1yr or 3yr", term)
}
