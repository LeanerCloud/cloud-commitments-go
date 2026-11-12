package savingsplans

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConvertTermToSeconds_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		term    string
		want    int64
		wantErr bool
	}{
		{"1yr", 31536000, false},
		{"1", 31536000, false},
		{"3yr", 94608000, false},
		{"3", 94608000, false},
		{"", 0, true},
		{"2yr", 0, true},
		{"3YR", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.term, func(t *testing.T) {
			got, err := convertTermToSeconds(tt.term)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConvertPaymentOption_Table(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in      string
		want    types.SavingsPlanPaymentOption
		wantErr bool
	}{
		{"all-upfront", types.SavingsPlanPaymentOptionAllUpfront, false},
		{"All Upfront", types.SavingsPlanPaymentOptionAllUpfront, false},
		{"partial-upfront", types.SavingsPlanPaymentOptionPartialUpfront, false},
		{"Partial Upfront", types.SavingsPlanPaymentOptionPartialUpfront, false},
		{"no-upfront", types.SavingsPlanPaymentOptionNoUpfront, false},
		{"No Upfront", types.SavingsPlanPaymentOptionNoUpfront, false},
		{"", "", true},
		{"monthly", "", true},
		{"ALL-UPFRONT", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := convertPaymentOption(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
