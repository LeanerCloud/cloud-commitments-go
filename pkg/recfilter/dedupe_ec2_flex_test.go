package recfilter

import (
	"math/rand"
	"strings"
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

func TestEC2SizeFlexPartialNodeNotCreditedTwice(t *testing.T) {
	t.Parallel()
	ri := []common.Commitment{flexCommitment("m5.xlarge", "linux", "default", "regional", "", 1)}
	// 8 units owned: a large (4) leaves 4, which is below one xlarge (8).
	for name, recs := range map[string][]common.Recommendation{
		"large then xlarge": {flexRec("m5.large", "linux", "default", 1), flexRec("m5.xlarge", "linux", "default", 1)},
		"xlarge then large": {flexRec("m5.xlarge", "linux", "default", 1), flexRec("m5.large", "linux", "default", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			passed := runEC2(t, nil, ri, recs...)
			require.Len(t, passed, 1)
			assert.Equal(t, 1, passed[0].Count)
		})
	}
}

func TestEC2SizeFlexSuppressedUnitsNeverExceedOwned(t *testing.T) {
	t.Parallel()
	sizes := []string{"large", "xlarge", "2xlarge"}
	units := map[string]int{"large": 4, "xlarge": 8, "2xlarge": 16}
	rng := rand.New(rand.NewSource(323))
	for iter := 0; iter < 300; iter++ {
		var commitments []common.Commitment
		owned := 0
		for i := rng.Intn(3) + 1; i > 0; i-- {
			sz, n := sizes[rng.Intn(3)], rng.Intn(3)+1
			commitments = append(commitments, flexCommitment("m5."+sz, "linux", "default", "regional", "", n))
			owned += units[sz] * n
		}
		var recs []common.Recommendation
		demanded := 0
		for i := rng.Intn(5) + 1; i > 0; i-- {
			sz, n := sizes[rng.Intn(3)], rng.Intn(3)+1
			recs = append(recs, flexRec("m5."+sz, "linux", "default", n))
			demanded += units[sz] * n
		}
		kept := 0
		for _, r := range runEC2(t, nil, commitments, recs...) {
			kept += units[strings.TrimPrefix(r.ResourceType, "m5.")] * r.Count
		}
		suppressed := demanded - kept
		require.LessOrEqual(t, suppressed, owned, "iter %d: suppressed %d units from %d owned", iter, suppressed, owned)
	}
}

func TestEC2SizeFlexExcludedFamiliesStayExact(t *testing.T) {
	t.Parallel()
	for _, family := range []string{"g5", "G4dn", "p5", "inf2", "u7i-6tb"} {
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			ri := []common.Commitment{flexCommitment(strings.ToLower(family)+".xlarge", "linux", "default", "regional", "", 1)}
			passed := runEC2(t, nil, ri, flexRec(strings.ToLower(family)+".large", "linux", "default", 2))
			require.Len(t, passed, 1)
			assert.Equal(t, 2, passed[0].Count)
			assert.Empty(t, runEC2(t, nil, ri, flexRec(strings.ToLower(family)+".xlarge", "linux", "default", 1)), "same type still dedupes")
		})
	}
}
