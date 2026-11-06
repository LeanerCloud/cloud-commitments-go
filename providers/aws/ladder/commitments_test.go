package ladder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

func TestRIToCommitment_ZeroComputedCostIsKnown(t *testing.T) {
	t.Parallel()
	// Fully-upfront RI with no duration, recurring or usage price: the
	// computed hourly cost is a real 0, not an unknown one.
	ri := &ec2svc.ConvertibleRI{ReservedInstanceID: "ri-1", InstanceCount: 2}

	got := riToCommitment(ri, "123456789012", "us-east-1")

	assert.True(t, got.CostKnown)
	assert.Zero(t, got.Cost)
}

func TestSPToCommitment_ZeroHourlyCommitmentIsKnown(t *testing.T) {
	t.Parallel()
	sp := &ActiveSP{PlanID: "sp-1", PlanType: spPlanTypeCompute, HourlyCommitmentUSD: 0}

	got := spToCommitment(sp, "123456789012")

	assert.True(t, got.CostKnown)
	assert.Zero(t, got.Cost)
}
