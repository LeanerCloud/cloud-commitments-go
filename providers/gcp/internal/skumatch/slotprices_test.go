package skumatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

func TestSlotPrices_Add(t *testing.T) {
	a := &cloudbilling.Sku{SkuId: "A", Description: "first"}
	b := &cloudbilling.Sku{SkuId: "B", Description: "second"}
	type add struct {
		slot  PriceSlot
		sku   *cloudbilling.Sku
		price float64
	}
	tests := []struct {
		name           string
		adds           []add
		wantOnDemand   float64
		wantCommitment float64
		wantErr        string
	}{
		{name: "one price per slot", adds: []add{{SlotOnDemand, a, 0.05}, {SlotCommitment, b, 0.04}}, wantOnDemand: 0.05, wantCommitment: 0.04},
		{name: "agreeing duplicates are accepted", adds: []add{{SlotOnDemand, a, 0.05}, {SlotOnDemand, b, 0.05}}, wantOnDemand: 0.05},
		{name: "conflicting on-demand is an error", adds: []add{{SlotOnDemand, a, 0.05}, {SlotOnDemand, b, 0.8}}, wantErr: "ambiguous catalog: on-demand SKU B"},
		{name: "conflicting on-demand in reverse order is an error", adds: []add{{SlotOnDemand, b, 0.8}, {SlotOnDemand, a, 0.05}}, wantErr: "ambiguous catalog: on-demand SKU A"},
		{name: "conflicting commitment is an error", adds: []add{{SlotCommitment, a, 0.04}, {SlotCommitment, b, 0.03}}, wantErr: "ambiguous catalog: commitment SKU B"},
		{name: "SlotNone is ignored", adds: []add{{SlotOnDemand, a, 0.05}, {SlotNone, b, 0.01}}, wantOnDemand: 0.05},
		{name: "slots do not interfere", adds: []add{{SlotOnDemand, a, 0.05}, {SlotCommitment, a, 0.04}, {SlotCommitment, b, 0.04}}, wantOnDemand: 0.05, wantCommitment: 0.04},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p SlotPrices
			var err error
			for _, ad := range tt.adds {
				if err = p.Add(ad.slot, ad.sku, ad.price); err != nil {
					break
				}
			}
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantOnDemand, p.OnDemand)
			assert.Equal(t, tt.wantCommitment, p.Commitment)
		})
	}
}
