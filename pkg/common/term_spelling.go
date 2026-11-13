package common

import "strings"

// TermSpelling selects which spellings of a term a caller accepts. Each
// caller keeps its own accepted set; the spellings and their meaning live
// here so they cannot drift apart.
type TermSpelling uint8

const (
	// SpellCanonical accepts "1yr" and "3yr".
	SpellCanonical TermSpelling = 1 << iota
	// SpellBareYears accepts "1" and "3".
	SpellBareYears
	// SpellShortYears accepts "1y" and "3y".
	SpellShortYears
	// SpellMonths accepts "12mo" and "36mo".
	SpellMonths
	// SpellEmptyIsOneYear maps the empty string to Term1Year.
	SpellEmptyIsOneYear
	// SpellFold trims surrounding whitespace and lower-cases before matching.
	SpellFold
)

// termSpellings maps each spelling to the term it means and the flag that
// enables it.
var termSpellings = map[string]struct {
	term Term
	flag TermSpelling
}{
	"":     {Term1Year, SpellEmptyIsOneYear},
	"1yr":  {Term1Year, SpellCanonical},
	"3yr":  {Term3Year, SpellCanonical},
	"1":    {Term1Year, SpellBareYears},
	"3":    {Term3Year, SpellBareYears},
	"1y":   {Term1Year, SpellShortYears},
	"3y":   {Term3Year, SpellShortYears},
	"12mo": {Term1Year, SpellMonths},
	"36mo": {Term3Year, SpellMonths},
}

// ParseTermSpelling maps s to a Term when s is one of the spellings selected
// by accept, and reports false otherwise.
func ParseTermSpelling(s string, accept TermSpelling) (Term, bool) {
	if accept&SpellFold != 0 {
		s = strings.ToLower(strings.TrimSpace(s))
	}
	e, ok := termSpellings[s]
	if !ok || accept&e.flag == 0 {
		return "", false
	}
	return e.term, true
}

// Years returns the term length in years, or 0 for an unrecognized Term.
func (t Term) Years() int {
	switch t {
	case Term1Year:
		return 1
	case Term3Year:
		return 3
	}
	return 0
}

// Months returns the term length in months, or 0 for an unrecognized Term.
func (t Term) Months() int { return t.Years() * 12 }
