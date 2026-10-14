package ladder

import (
	"context"
	"math"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

func TestPurchaseLayer_CommitmentPrecision(t *testing.T) {
	t.Parallel()
	for _, layer := range []ladder.LayerType{ladder.LayerComputeSP, ladder.LayerEC2InstanceSP} {
		for _, tc := range []struct{ amount, want float64 }{{.001, .001}, {.00499, .00499}, {.005, .005}, {1.234567, 1.23456}, {.29, .29}, {1000000, 1000000}, {math.Nextafter(1000000, 0), 999999.99999}} {
			t.Run(string(layer)+strconv.FormatFloat(tc.amount, 'g', -1, 64), func(t *testing.T) {
				planType := spPlanTypeCompute
				if layer == ladder.LayerEC2InstanceSP {
					planType = spPlanTypeEC2Instance
				}
				p := &fakePurchaser{}
				a := newWiredLadder(t, &fakePurchaser{}, p, &fakeExchangeRunner{})
				rec := validSPRec(planType)
				original := rec.Details.(*common.SavingsPlanDetails)
				original.HourlyCommitment = tc.amount
				_, err := a.PurchaseLayer(context.Background(), layer, rec, validPurchaseOpts())
				require.NoError(t, err)
				require.Equal(t, 1, p.calls)
				details := p.gotRec.Details.(*common.SavingsPlanDetails)
				assert.Equal(t, tc.want, details.HourlyCommitment)
				assert.NotSame(t, original, details)
				assert.Equal(t, tc.amount, original.HourlyCommitment)
				assert.Equal(t, "ladder-tok-1", p.gotOpts.IdempotencyToken)
			})
		}
	}
}

func TestPurchaseLayer_CommitmentPrecisionRefusesInvalid(t *testing.T) {
	t.Parallel()
	for _, amount := range []float64{.00099, math.Nextafter(.001, 0), math.Nextafter(1000000, math.Inf(1)), 1000001, math.Inf(-1)} {
		t.Run(strconv.FormatFloat(amount, 'g', -1, 64), func(t *testing.T) {
			p := &fakePurchaser{}
			a := newWiredLadder(t, &fakePurchaser{}, p, &fakeExchangeRunner{})
			rec := validSPRec(spPlanTypeCompute)
			rec.Details.(*common.SavingsPlanDetails).HourlyCommitment = amount
			_, err := a.PurchaseLayer(context.Background(), ladder.LayerComputeSP, rec, validPurchaseOpts())
			require.Error(t, err)
			assert.Zero(t, p.calls)
		})
	}
	for _, details := range []common.ServiceDetails{nil, (*common.SavingsPlanDetails)(nil)} {
		p := &fakePurchaser{}
		a := newWiredLadder(t, &fakePurchaser{}, p, &fakeExchangeRunner{})
		rec := validSPRec(spPlanTypeCompute)
		rec.Details = details
		require.NotPanics(t, func() {
			_, err := a.PurchaseLayer(context.Background(), ladder.LayerComputeSP, rec, validPurchaseOpts())
			require.Error(t, err)
		})
		assert.Zero(t, p.calls)
	}
}
