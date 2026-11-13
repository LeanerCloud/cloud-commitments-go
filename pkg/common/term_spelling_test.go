package common

import (
	"strings"
	"testing"
)

func TestParseTermSpelling(t *testing.T) {
	all := SpellCanonical | SpellBareYears | SpellShortYears | SpellMonths | SpellEmptyIsOneYear
	cases := []struct {
		in     string
		accept TermSpelling
		want   Term
		ok     bool
	}{
		{"1yr", SpellCanonical, Term1Year, true},
		{"3yr", SpellCanonical, Term3Year, true},
		{"1", SpellCanonical, "", false},
		{"1", SpellBareYears, Term1Year, true},
		{"3", SpellBareYears, Term3Year, true},
		{"1y", SpellShortYears, Term1Year, true},
		{"3y", SpellCanonical | SpellBareYears, "", false},
		{"12mo", SpellMonths, Term1Year, true},
		{"36mo", SpellMonths, Term3Year, true},
		{"", SpellCanonical, "", false},
		{"", SpellEmptyIsOneYear, Term1Year, true},
		{" 3YR ", SpellCanonical, "", false},
		{" 3YR ", SpellCanonical | SpellFold, Term3Year, true},
		{"5yr", all | SpellFold, "", false},
	}
	for _, c := range cases {
		got, ok := ParseTermSpelling(c.in, c.accept)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseTermSpelling(%q, %b) = %q, %v; want %q, %v", c.in, c.accept, got, ok, c.want, c.ok)
		}
	}
}

func TestTermYearsMonths(t *testing.T) {
	if Term1Year.Years() != 1 || Term3Year.Years() != 3 || Term("x").Years() != 0 {
		t.Error("Years mismatch")
	}
	if Term1Year.Months() != 12 || Term3Year.Months() != 36 || Term("").Months() != 0 {
		t.Error("Months mismatch")
	}
}

func TestParseTermSpellingBareMonthsAndISO(t *testing.T) {
	cases := []struct {
		in     string
		accept TermSpelling
		want   Term
		ok     bool
	}{
		{"12", SpellBareMonths, Term1Year, true},
		{"36", SpellBareMonths, Term3Year, true},
		{"12", SpellCanonical | SpellBareYears, "", false},
		{"24", SpellBareMonths, "", false},
		{"P1Y", SpellISO, Term1Year, true},
		{"P3Y", SpellISO, Term3Year, true},
		{"p1y", SpellISO, "", false},
		{"p1y", SpellISO | SpellFold, Term1Year, true},
		{" P3Y ", SpellISO | SpellFold, Term3Year, true},
		{"P1Y", SpellCanonical, "", false},
		{"P2Y", SpellISO | SpellFold, "", false},
	}
	for _, c := range cases {
		got, ok := ParseTermSpelling(c.in, c.accept)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseTermSpelling(%q, %b) = %q, %v; want %q, %v", c.in, c.accept, got, ok, c.want, c.ok)
		}
	}
}

// oldNormalizeReservationTerm is the pre-migration switch, kept to prove the
// shared parser accepts exactly the same inputs with the same results.
func oldNormalizeReservationTerm(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(Term1Year), "1", "1y", "p1y":
		return string(Term1Year)
	case string(Term3Year), "3", "3y", "p3y":
		return string(Term3Year)
	default:
		return normalizeReservationSegment(s)
	}
}

func TestNormalizeReservationTermMatchesOld(t *testing.T) {
	inputs := []string{"", "1", "3", "1yr", "3yr", "1y", "3y", "P1Y", "p1y", "P3Y", "p3y", " 3YR ", "1YR", "12", "36", "12mo", "36mo", "5yr", "2", "db.t4g", "P2Y", "\t1\n"}
	for _, in := range inputs {
		if got, want := normalizeReservationTerm(in), oldNormalizeReservationTerm(in); got != want {
			t.Errorf("normalizeReservationTerm(%q) = %q, old %q", in, got, want)
		}
	}
}
