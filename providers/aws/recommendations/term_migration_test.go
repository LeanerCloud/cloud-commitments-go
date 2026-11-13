package recommendations

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// oldRIRequestTerm is the pre-migration switch, kept so the differential test
// proves the shared parser keeps the accepted inputs and results.
func oldRIRequestTerm(term string) (types.TermInYears, float64, bool) {
	switch common.Term(term) {
	case common.Term1Year, "1":
		return types.TermInYearsOneYear, 12, true
	case common.Term3Year, "3":
		return types.TermInYearsThreeYears, 36, true
	}
	return "", 0, false
}

func TestTermParsersMatchOld(t *testing.T) {
	inputs := []string{"", "1", "3", "1yr", "3yr", "1y", "3y", "12", "36", "12mo", "36mo", "P1Y", "1YR", " 1yr", "5yr", "2"}
	for _, in := range inputs {
		wantYears, wantMonths, wantOK := oldRIRequestTerm(in)
		gotYears, gotMonths, err := riRequestTerm(in)
		assert.Equal(t, wantOK, err == nil, "riRequestTerm(%q) acceptance", in)
		assert.Equal(t, wantYears, gotYears, "riRequestTerm(%q) years", in)
		assert.Equal(t, wantMonths, gotMonths, "riRequestTerm(%q) months", in)
		convYears, convErr := convertTermInYearsE(in)
		assert.Equal(t, wantOK, convErr == nil, "convertTermInYearsE(%q) acceptance", in)
		assert.Equal(t, wantYears, convYears, "convertTermInYearsE(%q) years", in)
	}
}
