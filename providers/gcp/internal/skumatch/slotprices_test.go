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
	// SKUs without an ID must still mark their slot as filled.
	noID1 := &cloudbilling.Sku{Description: "no id one"}
	noID2 := &cloudbilling.Sku{Description: "no id two"}
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
		{name: "one price per slot", adds: []add{{SlotOnDemand, a, 0.05}, {SlotCommit1Yr, b, 0.04}}, wantOnDemand: 0.05, wantCommitment: 0.04},
		{name: "agreeing duplicates are accepted", adds: []add{{SlotOnDemand, a, 0.05}, {SlotOnDemand, b, 0.05}}, wantOnDemand: 0.05},
		{name: "conflicting on-demand is an error", adds: []add{{SlotOnDemand, a, 0.05}, {SlotOnDemand, b, 0.8}}, wantErr: "ambiguous catalog: on-demand SKU B"},
		{name: "conflicting on-demand in reverse order is an error", adds: []add{{SlotOnDemand, b, 0.8}, {SlotOnDemand, a, 0.05}}, wantErr: "ambiguous catalog: on-demand SKU A"},
		{name: "conflicting commitment is an error", adds: []add{{SlotCommit1Yr, a, 0.04}, {SlotCommit1Yr, b, 0.03}}, wantErr: "ambiguous catalog: 1-year commitment SKU B"},
		{name: "SlotNone is ignored", adds: []add{{SlotOnDemand, a, 0.05}, {SlotNone, b, 0.01}}, wantOnDemand: 0.05},
		{name: "conflicting on-demand without SKU IDs is an error", adds: []add{{SlotOnDemand, noID1, 0.05}, {SlotOnDemand, noID2, 0.8}}, wantErr: "ambiguous catalog: on-demand SKU  (\"no id two\")"},
		{name: "conflicting commitment without SKU IDs is an error", adds: []add{{SlotCommit1Yr, noID1, 0.04}, {SlotCommit1Yr, noID2, 0.03}}, wantErr: "ambiguous catalog: 1-year commitment SKU  (\"no id two\")"},
		{name: "ID-less SKU then a conflicting SKU is an error", adds: []add{{SlotOnDemand, noID1, 0.05}, {SlotOnDemand, b, 0.8}}, wantErr: "ambiguous catalog: on-demand SKU B"},
		{name: "agreeing duplicates without SKU IDs are accepted", adds: []add{{SlotOnDemand, noID1, 0.05}, {SlotOnDemand, noID2, 0.05}}, wantOnDemand: 0.05},
		{name: "slots do not interfere", adds: []add{{SlotOnDemand, a, 0.05}, {SlotCommit1Yr, a, 0.04}, {SlotCommit1Yr, b, 0.04}}, wantOnDemand: 0.05, wantCommitment: 0.04},
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
			assert.Equal(t, tt.wantCommitment, p.commit1Yr)
		})
	}
}
