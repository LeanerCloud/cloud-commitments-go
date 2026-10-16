package reservations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReservationTermForYears(t *testing.T) {
	for years, want := range map[int]string{1: "P1Y", 3: "P3Y"} {
		got, err := ReservationTermForYears(years)
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
	for _, years := range []int{0, 5} {
		_, err := ReservationTermForYears(years)
		assert.Error(t, err, "years=%d", years)
	}
}
