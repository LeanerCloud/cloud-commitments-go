package azure

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// oldAdvisorTerm is the pre-migration switch, kept so the differential test
// proves the shared parser keeps the accepted inputs, results and errors.
func oldAdvisorTerm(ext map[string]*string) (common.Term, error) {
	switch t := extString(ext, advisorKeyTerm); t {
	case "P1Y", string(common.Term1Year):
		return common.Term1Year, nil
	case "P3Y", string(common.Term3Year):
		return common.Term3Year, nil
	case "":
		return "", fmt.Errorf("%s is absent", advisorKeyTerm)
	default:
		return "", fmt.Errorf("unknown %s %q", advisorKeyTerm, t)
	}
}

func TestAdvisorTermMatchesOld(t *testing.T) {
	for _, in := range []string{"P1Y", "P3Y", "1yr", "3yr", "", "p1y", "1", "3", "1y", "12mo", "12", "36", "P5Y", " 1yr", "1YR"} {
		ext := map[string]*string{advisorKeyTerm: strPtr(in)}
		wantTerm, wantErr := oldAdvisorTerm(ext)
		gotTerm, gotErr := advisorTerm(ext)
		assert.Equal(t, wantTerm, gotTerm, "term for %q", in)
		assert.Equal(t, wantErr, gotErr, "error for %q", in)
	}
	wantTerm, wantErr := oldAdvisorTerm(map[string]*string{})
	gotTerm, gotErr := advisorTerm(map[string]*string{})
	assert.Equal(t, wantTerm, gotTerm)
	assert.Equal(t, wantErr, gotErr)
}
