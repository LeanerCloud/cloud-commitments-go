package insurance

import (
	"errors"
	"math/big"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrUnrepresentable is returned when a vendor number has no exact finite
// decimal form (the DTO never rounds or converts to float).
var ErrUnrepresentable = errors.New("archera value has no exact decimal representation")

const maxVendorStrLen = 256

// Decimal renders r as the shortest exact decimal string. nil (unknown) stays
// nil. The denominator of a reduced rational is finite in decimal exactly when
// it has no prime factor other than 2 and 5, and the digits needed equal
// max(power of 2, power of 5).
func Decimal(r *big.Rat) (*string, error) {
	if r == nil {
		return nil, nil
	}
	d := new(big.Int).Set(r.Denom())
	two, five, zero := big.NewInt(2), big.NewInt(5), new(big.Int)
	count := func(p *big.Int) int {
		n, m := 0, new(big.Int)
		for {
			q, rem := new(big.Int).QuoRem(d, p, m)
			if rem.Cmp(zero) != 0 {
				return n
			}
			d, n = q, n+1
		}
	}
	c2, c5 := count(two), count(five)
	if d.Cmp(big.NewInt(1)) != 0 {
		return nil, ErrUnrepresentable
	}
	s := r.FloatString(max(c2, c5))
	return &s, nil
}

// CleanString removes control, format and line/paragraph-separator characters
// from vendor text and caps it at 256 bytes on a rune boundary.
func CleanString(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxVendorStrLen {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}
