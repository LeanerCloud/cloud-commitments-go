package recommendations

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSPRecurringMonthlyCost_PaymentOption(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, payment string
		want          float64
		wantErr       bool
	}{
		{"all-upfront", "all-upfront", 0, false},
		{"partial-upfront", "partial-upfront", 365, false},
		{"no-upfront", "no-upfront", 730, false},
		{"empty", "", 0, true},
		{"unknown", "monthly", 0, true},
		{"wrong case", "All-Upfront", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := spRecurringMonthlyCost(1, 0, "1yr", tt.payment)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tt.payment == "partial-upfront" {
				got, err = spRecurringMonthlyCost(1, 13140, "3yr", tt.payment)
				require.NoError(t, err)
			}
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

func TestRIRequestTerm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		term    string
		want    types.TermInYears
		months  float64
		wantErr bool
	}{
		{"1yr", types.TermInYearsOneYear, 12, false},
		{"1", types.TermInYearsOneYear, 12, false},
		{"3yr", types.TermInYearsThreeYears, 36, false},
		{"3", types.TermInYearsThreeYears, 36, false},
		{"", "", 0, true},
		{"5yr", "", 0, true},
		{"1YR", "", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.term, func(t *testing.T) {
			got, months, err := riRequestTerm(tt.term)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.months, months)
		})
	}
}
