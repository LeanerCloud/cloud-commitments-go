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
	}

	got := riToCommitment(ri, "123456789012", "us-east-1")

	assert.True(t, got.CostKnown)
	assert.Greater(t, got.Cost, 0.0)
}

func TestRIToCommitment_NoDurationIsUnknown(t *testing.T) {
	t.Parallel()
	// Without a duration the upfront part cannot be amortized, so the
	// computed cost is incomplete and must not be claimed as known.
	ri := &ec2svc.ConvertibleRI{ReservedInstanceID: "ri-1", InstanceCount: 2, FixedPrice: 8760}

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
