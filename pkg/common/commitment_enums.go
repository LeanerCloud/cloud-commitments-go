package common

import "fmt"

// Term is the commitment duration of a purchase action.
type Term string

const (
	Term1Year Term = "1yr"
	Term3Year Term = "3yr"
)

// Validate returns an error when t is not a recognized Term.
func (t Term) Validate() error {
	switch t {
	case Term1Year, Term3Year:
		return nil
	}
	return fmt.Errorf("unknown term %q", t)
}

// ParseTerm converts s into a Term, returning a descriptive error when s is
// not a recognized value.
func ParseTerm(s string) (Term, error) {
	t := Term(s)
	if err := t.Validate(); err != nil {
		return "", err
	}
	return t, nil
}

// PaymentOption is the payment structure of a purchase action.
type PaymentOption string

const (
	PaymentAllUpfront     PaymentOption = "all-upfront"
	PaymentPartialUpfront PaymentOption = "partial-upfront"
	PaymentNoUpfront      PaymentOption = "no-upfront"
)

// Validate returns an error when p is not a recognized PaymentOption.
func (p PaymentOption) Validate() error {
	switch p {
	case PaymentAllUpfront, PaymentPartialUpfront, PaymentNoUpfront:
		return nil
	}
	return fmt.Errorf("unknown payment option %q", p)
}

// ParsePaymentOption converts s into a PaymentOption, returning a
// descriptive error when s is not a recognized value.
func ParsePaymentOption(s string) (PaymentOption, error) {
	p := PaymentOption(s)
	if err := p.Validate(); err != nil {
		return "", err
	}
	return p, nil
}
