package purchasecfg

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseTermMonths(t *testing.T) {
	tests := []struct {
		name      string
		term      string
		expected  int
		expectErr bool
	}{
		{"1 year", "1yr", 12, false},
		{"1 numeric", "1", 12, false},
		{"3 years", "3yr", 36, false},
		{"3 numeric", "3", 36, false},
		{"invalid term errors", "invalid", 0, true},
		{"empty term errors", "", 0, true},
		{"zero term errors", "0", 0, true},
		{"2yr term errors", "2yr", 0, true},
		{"month-count form not in canonical domain", "12", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseTermMonths(tt.term, "Test")
			if tt.expectErr {
				if assert.Error(t, err) {
					assert.Contains(t, err.Error(), "unsupported Test reservation term")
				}
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDurationSeconds(t *testing.T) {
	one, err := DurationSeconds("1yr", "Test")
	assert.NoError(t, err)
	assert.Equal(t, int64(OneYearSeconds), one)

	three, err := DurationSeconds("3", "Test")
	assert.NoError(t, err)
	assert.Equal(t, int64(ThreeYearSeconds), three)

	_, err = DurationSeconds("2yr", "Test")
	assert.Error(t, err)
}

func TestDurationSecondsString(t *testing.T) {
	one, err := DurationSecondsString("1", "Test")
	assert.NoError(t, err)
	assert.Equal(t, "31536000", one)

	three, err := DurationSecondsString("3yr", "Test")
	assert.NoError(t, err)
	assert.Equal(t, "94608000", three)

	_, err = DurationSecondsString("", "Test")
	assert.Error(t, err)
}

func TestDurationYearString(t *testing.T) {
	one, err := DurationYearString("1yr", "Test")
	assert.NoError(t, err)
	assert.Equal(t, "1yr", one)

	three, err := DurationYearString("3yr", "Test")
	assert.NoError(t, err)
	assert.Equal(t, "3yr", three)

	_, err = DurationYearString("invalid", "Test")
	assert.Error(t, err)
}
