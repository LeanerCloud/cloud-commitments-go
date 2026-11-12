package common

import "testing"

func TestParseTerm(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    Term
		wantErr bool
	}{
		{"1yr", "1yr", Term1Year, false},
		{"3yr", "3yr", Term3Year, false},
		{"empty", "", "", true},
		{"unknown", "5yr", "", true},
		{"wrong case", "1YR", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseTerm(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParseTerm(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("ParseTerm(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestParsePaymentOption(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    PaymentOption
		wantErr bool
	}{
		{"all upfront", "all-upfront", PaymentAllUpfront, false},
		{"partial upfront", "partial-upfront", PaymentPartialUpfront, false},
		{"no upfront", "no-upfront", PaymentNoUpfront, false},
		{"empty", "", "", true},
		{"unknown", "monthly", "", true},
		{"wrong separator", "all_upfront", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePaymentOption(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("ParsePaymentOption(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
			if got != c.want {
				t.Errorf("ParsePaymentOption(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
