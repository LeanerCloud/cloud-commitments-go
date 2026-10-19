package pricing

import (
	"fmt"
	"regexp"
	"strings"
)

// odataTokenRE is the character set of every Azure SKU and region name the
// Retail Prices filters are built from. Anything else is rejected rather than
// escaped, so a value can never change the shape of the $filter.
var odataTokenRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ODataString returns v as a single-quoted OData string literal for a Retail
// Prices $filter. It rejects values outside [A-Za-z0-9_.-]; the quote doubling
// is defense in depth should the allowed set ever grow.
func ODataString(v string) (string, error) {
	if !odataTokenRE.MatchString(v) {
		return "", fmt.Errorf("invalid value %q for Retail Prices filter: must match %s", v, odataTokenRE)
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
}
