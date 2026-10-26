package skumatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTermYears(t *testing.T) {
	accepted := map[string]int{
		"": 1, "1yr": 1, "1": 1, "12mo": 1, "1YR": 1, "1Yr": 1, " 1yr ": 1, "12MO": 1, " 12mo": 1,
		"3yr": 3, "3": 3, "36mo": 3, "3YR": 3, " 3yr": 3, "36MO": 3, " 36mo ": 3, " 3 ": 3,
	}
	for term, want := range accepted {
		got, err := TermYears(term)
		require.NoError(t, err, term)
		assert.Equal(t, want, got, term)
	}
	for _, term := range []string{"2yr", "2", "5yr", "3yrs", "24mo", "P1Y", "abc", "monthly"} {
		_, err := TermYears(term)
		require.Error(t, err, term)
		assert.Contains(t, err.Error(), "unsupported commitment term")
	}
}
