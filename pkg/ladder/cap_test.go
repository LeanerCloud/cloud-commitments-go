package ladder

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequireCap(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		name    string
		in      *float64
		want    float64
		wantErr string
	}{
		{"nil", nil, 0, "Cap is required: set an explicit cap (a nil cap is not treated as unlimited)"},
		{"NaN", f(math.NaN()), 0, "Cap must be finite, got NaN"},
		{"+Inf", f(math.Inf(1)), 0, "Cap must be finite, got +Inf"},
		{"-Inf", f(math.Inf(-1)), 0, "Cap must be finite, got -Inf"},
		{"zero", f(0), 0, "Cap must be > 0 (a zero cap would block every exchange), got 0"},
		{"negative", f(-5), 0, "Cap must be > 0 (a zero cap would block every exchange), got -5"},
		{"smallest positive", f(5e-324), 5e-324, ""},
		{"valid", f(12.5), 12.5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := RequireCap("Cap", tt.in)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Equal(t, tt.wantErr, err.Error())
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
