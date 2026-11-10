package ladder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

func TestRIToCommitment_FullyUpfrontWithDurationIsKnown(t *testing.T) {
	t.Parallel()
	ri := &ec2svc.ConvertibleRI{
		ReservedInstanceID: "ri-1",
		InstanceCount:      2,
		FixedPrice:         8760,
		Duration:           31536000, // one year in seconds
		PriceReported:      true,
	}

	got := riToCommitment(ri, "123456789012", "us-east-1")

	assert.True(t, got.CostKnown)
	assert.Greater(t, got.Cost, 0.0)
}

func TestRIToCommitment_NoDurationIsUnknown(t *testing.T) {
	t.Parallel()
	// Without a duration the upfront part cannot be amortized, so the
	// computed cost is incomplete and must not be claimed as known.
	ri := &ec2svc.ConvertibleRI{ReservedInstanceID: "ri-1", InstanceCount: 2, FixedPrice: 8760, PriceReported: true}

	got := riToCommitment(ri, "123456789012", "us-east-1")

	assert.False(t, got.CostKnown)
}

func TestSPToCommitment_ZeroHourlyCommitmentIsKnown(t *testing.T) {
	t.Parallel()
	sp := &ActiveSP{PlanID: "sp-1", PlanType: spPlanTypeCompute, HourlyCommitmentUSD: 0}

	got := spToCommitment(sp, "123456789012")

	assert.True(t, got.CostKnown)
	assert.Zero(t, got.Cost)
}

func TestRIToCommitment_CostKnown(t *testing.T) {
	t.Parallel()
	const year = 31536000
	tests := []struct {
		name     string
		ri       ec2svc.ConvertibleRI
		wantKnow bool
		wantCost float64
	}{
		{"nothing reported with duration", ec2svc.ConvertibleRI{InstanceCount: 1, Duration: year}, false, 0},
		{"no upfront, recurring reported, no duration", ec2svc.ConvertibleRI{InstanceCount: 2, RecurringHourlyAmount: 0.5, PriceReported: true}, true, 1.0},
		{"upfront with duration", ec2svc.ConvertibleRI{InstanceCount: 1, FixedPrice: 8760, Duration: year, PriceReported: true}, true, 1.0},
		{"upfront without duration", ec2svc.ConvertibleRI{InstanceCount: 1, FixedPrice: 8760, PriceReported: true}, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := riToCommitment(&tt.ri, "123456789012", "us-east-1")
			assert.Equal(t, tt.wantKnow, got.CostKnown)
			if tt.wantKnow {
				assert.InDelta(t, tt.wantCost, got.Cost, 1e-9)
			}
		})
	}
}
