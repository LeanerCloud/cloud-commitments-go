package recfilter

import (
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func flexCommitment(instanceType, platform, tenancy, scope, az string, count int) common.Commitment {
	c := ec2Commitment(platform, tenancy, scope, az, count)
	c.ResourceType = instanceType
	return c
}

func flexRec(instanceType, platform, tenancy string, count int) common.Recommendation {
	r := ec2Rec(platform, tenancy, "regional", "", count)
	r.ResourceType = instanceType
	r.Details.(*common.ComputeDetails).InstanceType = instanceType
	return r
}

func TestEC2SizeFlexDedupe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		commitment common.Commitment
		rec        common.Recommendation
		want       int
	}{
		{"regional linux default xlarge covers two larges", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.large", "linux", "default", 2), 0},
		{"covers one of three larges", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.large", "linux", "default", 3), 1},
		{"small RI does not cover bigger rec", flexCommitment("m5.large", "linux", "default", "regional", "", 1), flexRec("m5.xlarge", "linux", "default", 1), 1},
		{"two larges cover an xlarge", flexCommitment("m5.large", "linux", "default", "regional", "", 2), flexRec("m5.xlarge", "linux", "default", 1), 0},
		{"other family", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("c5.large", "linux", "default", 1), 1},
		{"zonal RI is exact", flexCommitment("m5.xlarge", "linux", "default", "zonal", "us-east-1a", 1), flexRec("m5.large", "linux", "default", 2), 2},
		{"windows RI is exact", flexCommitment("m5.xlarge", "windows", "default", "regional", "", 1), flexRec("m5.large", "windows", "default", 2), 2},
		{"dedicated RI is exact", flexCommitment("m5.xlarge", "linux", "dedicated", "regional", "", 1), flexRec("m5.large", "linux", "dedicated", 2), 2},
		{"unknown size is exact", flexCommitment("m5.metal", "linux", "default", "regional", "", 1), flexRec("m5.large", "linux", "default", 1), 1},
		{"unknown size same type still dedupes", flexCommitment("m5.metal", "linux", "default", "regional", "", 1), flexRec("m5.metal", "linux", "default", 1), 0},
		{"unknown rec size spends no flex budget", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.metal", "linux", "default", 1), 1},
		{"wildcard platform RI is exact", flexCommitment("m5.xlarge", "", "default", "regional", "", 1), flexRec("m5.large", "linux", "default", 2), 2},
		{"wildcard platform RI still suppresses its type", flexCommitment("m5.xlarge", "", "default", "regional", "", 1), flexRec("m5.xlarge", "linux", "default", 1), 0},
		{"wildcard scope RI is exact", flexCommitment("m5.xlarge", "linux", "default", "", "", 1), flexRec("m5.large", "linux", "default", 2), 2},
		{"wildcard rec platform spends no flex budget", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.large", "", "default", 2), 2},
		{"wildcard rec tenancy spends no flex budget", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.large", "linux", "", 2), 2},
		{"windows rec not covered by linux flex", flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1), flexRec("m5.large", "windows", "default", 2), 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			passed := runEC2(t, nil, []common.Commitment{tt.commitment}, tt.rec)
			if tt.want == 0 {
				assert.Empty(t, passed)
				return
			}
			require.Len(t, passed, 1)
			assert.Equal(t, tt.want, passed[0].Count)
		})
	}
}

func TestEC2SizeFlexBudgetSharedAcrossRecs(t *testing.T) {
	t.Parallel()
	passed := runEC2(t, nil, []common.Commitment{flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1)},
		flexRec("m5.large", "linux", "default", 1), flexRec("m5.large", "linux", "default", 1), flexRec("m5.large", "linux", "default", 1))
	assert.Len(t, passed, 1)
}
