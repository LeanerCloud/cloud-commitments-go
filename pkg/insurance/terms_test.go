package insurance

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinnedContractTerms is a second copy of the list in terms.go, not an
// independent check: no schema transcription or fixture exists in the repo
// (#285 added the code). It only catches an accidental edit of the source set.
// The absence of "twelve_month" and "twenty_four_month" is unverified against
// the live API.
var pinnedContractTerms = []string{
	"eight_month", "eight_month_gris", "eighteen_month", "eighteen_month_gris",
	"eleven_month", "eleven_month_gris", "fifteen_month", "fifteen_month_gris",
	"five_month", "five_month_gris", "five_year", "four_month", "four_month_gris",
	"fourteen_month", "fourteen_month_gris", "nine_month", "nine_month_gris",
	"nineteen_month", "nineteen_month_gris", "one_year", "one_year_gris",
	"seven_month", "seven_month_gris", "seventeen_month", "seventeen_month_gris",
	"six_month", "six_month_gris", "sixteen_month", "sixteen_month_gris",
	"ten_month", "ten_month_gris", "thirteen_month", "thirteen_month_gris",
	"thirty_day", "thirty_day_gris", "thirty_five_month", "thirty_five_month_gris",
	"thirty_four_month", "thirty_four_month_gris", "thirty_month", "thirty_month_gris",
	"thirty_one_month", "thirty_one_month_gris", "thirty_three_month", "thirty_three_month_gris",
	"thirty_two_month", "thirty_two_month_gris", "three_month", "three_month_gris",
	"three_year", "twelve_month_gris", "twenty_eight_month", "twenty_eight_month_gris",
	"twenty_five_month", "twenty_five_month_gris", "twenty_four_month_gris",
	"twenty_month", "twenty_month_gris", "twenty_nine_month", "twenty_nine_month_gris",
	"twenty_one_month", "twenty_one_month_gris", "twenty_seven_month", "twenty_seven_month_gris",
	"twenty_six_month", "twenty_six_month_gris", "twenty_three_month", "twenty_three_month_gris",
	"twenty_two_month", "twenty_two_month_gris", "two_month", "two_month_gris",
	"two_year", "zero_day",
}

func TestContractTerms_PinnedSortedUnique(t *testing.T) {
	got := ContractTerms()
	assert.Len(t, got, 74)
	assert.True(t, slices.IsSorted(got))
	assert.Equal(t, pinnedContractTerms, got)
	assert.Equal(t, got, ContractTerms(), "deterministic across calls")
}

func TestContractTerms_ReturnsACopy(t *testing.T) {
	first := ContractTerms()
	first[0] = "mutated"
	first = append(first[:1], "extra")
	_ = first
	assert.Equal(t, pinnedContractTerms, ContractTerms())
}

// Every exported term is accepted by the decoder check and by the request
// validation; clearly invalid terms are rejected before any request.
func TestContractTerms_MatchValidators(t *testing.T) {
	var calls atomic.Int32
	c := testClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(validComparisonJSON()))
	}))
	for _, term := range ContractTerms() {
		term := term
		_, err := nullableTerm(&term)
		require.NoError(t, err, term)
		_, err = c.Comparison(context.Background(), ComparisonRequest{PlanID: testPlan, ContractTerms: []string{term}})
		require.NoError(t, err, term)
	}
	assert.EqualValues(t, len(ContractTerms()), calls.Load())

	before := calls.Load()
	for _, bad := range []string{"one_decade", "forever"} {
		_, err := nullableTerm(&bad)
		require.Error(t, err, bad)
		_, err = c.Comparison(context.Background(), ComparisonRequest{PlanID: testPlan, ContractTerms: []string{bad}})
		require.Error(t, err, bad)
	}
	assert.Equal(t, before, calls.Load(), "invalid terms must not reach the server")
}
