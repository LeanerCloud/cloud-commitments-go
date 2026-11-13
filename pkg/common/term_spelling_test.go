package common

import "testing"

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
