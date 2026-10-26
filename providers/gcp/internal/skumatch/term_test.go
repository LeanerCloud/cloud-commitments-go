package skumatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTermYears(t *testing.T) {
	for term, want := range map[string]int{"": 1, "1yr": 1, "1": 1, "3yr": 3, "3": 3} {
		got, err := TermYears(term)
		require.NoError(t, err, term)
		assert.Equal(t, want, got, term)
	}
	for _, term := range []string{"2yr", "2", "5yr", "3yrs", "1Yr", " 3yr", "monthly"} {
		_, err := TermYears(term)
		require.Error(t, err, term)
		assert.Contains(t, err.Error(), "unsupported commitment term")
	}
}
