package insurance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecimal(t *testing.T) {
	for in, want := range map[string]string{
		"0": "0", "110": "110", "-5.5": "-5.5", "104.5": "104.5", "0.3": "0.3",
		"1e100":     "1" + strings.Repeat("0", 100),
		"1e-100":    "0." + strings.Repeat("0", 99) + "1",
		"-1.5e-100": "-0." + strings.Repeat("0", 99) + "15",
		"1/8":       "0.125", "7/25": "0.28",
	} {
		got, err := Decimal(rat(t, in))
		require.NoError(t, err, in)
		require.NotNil(t, got)
		assert.Equal(t, want, *got, in)
	}
}

func TestDecimal_NilIsUnknownNotZero(t *testing.T) {
	got, err := Decimal(nil)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestDecimal_NonTerminatingFailsLoud(t *testing.T) {
	for _, in := range []string{"1/3", "2/7", "1/6"} {
		got, err := Decimal(rat(t, in))
		assert.ErrorIs(t, err, ErrUnrepresentable, in)
		assert.Nil(t, got)
	}
}

func TestCleanString(t *testing.T) {
	assert.Equal(t, "abc", CleanString("a\x00b\u200bc\u2028\n\t"))
	assert.Equal(t, "plain text", CleanString("plain text"))
	long := strings.Repeat("a", 300)
	assert.Len(t, CleanString(long), 256)
	// 3-byte runes: 256/3 = 85 runes = 255 bytes, never a split rune.
	got := CleanString(strings.Repeat("€", 100))
	assert.Len(t, got, 255)
	assert.True(t, strings.HasPrefix(got, "€"))
}
