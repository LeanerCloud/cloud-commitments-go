package skumatch

import (
	"fmt"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// TermYears parses a recommendation term label into commitment years. It is
// case-insensitive, ignores surrounding whitespace, and accepts "1yr", "1",
// "12mo" and "" (an unset term has always meant one year) for one year and
// "3yr", "3", "36mo" for three. Anything else is an error: guessing a term
// would price the wrong commitment.
func TermYears(term string) (int, error) {
	t, ok := common.ParseTermSpelling(term, common.SpellCanonical|common.SpellBareYears|common.SpellMonths|common.SpellEmptyIsOneYear|common.SpellFold)
	if !ok {
		return 0, fmt.Errorf("unsupported commitment term %q, want 1yr or 3yr", term)
	}
	return t.Years(), nil
}
