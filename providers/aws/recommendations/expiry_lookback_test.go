package recommendations

import (
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/recfilter"
)

const lookbackDays = 30

func daysFromNow(d int) time.Time { return time.Now().Add(time.Duration(d) * 24 * time.Hour) }

func ec2Commit(count int, state common.CommitmentState, start, end time.Time) common.Commitment {
	return common.Commitment{
		Service: common.ServiceEC2, Region: "us-east-1", ResourceType: "m5.large",
		Count: count, State: state, StartDate: start, EndDate: end,
	}
}

func expiringRI(count int) common.Commitment {
	return ec2Commit(count, "active", daysFromNow(-300), daysFromNow(15))
}

func replacementRI(count int, startedDaysAgo int) common.Commitment {
	return ec2Commit(count, "active", daysFromNow(-startedDaysAgo), daysFromNow(300))
}

// runLookback runs the options API on a fresh expiryPrecisionFixture and
// returns the recs before and after the adjustment, plus the sized result.
func runLookback(t *testing.T, commits []common.Commitment, opts PoolExpiryOptions, mutate func(PoolCoverageMap)) (
	before, after []common.Recommendation, adjusted, missing int, sized []common.Recommendation,
) {
	t.Helper()
	recs, _, coverage := expiryPrecisionFixture()
	if mutate != nil {
		mutate(coverage)
	}
	ApplyCoverageMapToRecommendations(recs, coverage)
	before = make([]common.Recommendation, len(recs))
	copy(before, recs)
	adjusted, missing = AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(recs, commits, coverage, opts)
	return before, recs, adjusted, missing, recfilter.ApplyTargetCoverage(recs, 80, nil, nil)
}

func defaultOpts() PoolExpiryOptions {
	return PoolExpiryOptions{WindowDays: 30, CoverageLookbackDays: lookbackDays}
}

func counts(recs []common.Recommendation) []int {
	out := make([]int, len(recs))
	for i := range recs {
		out[i] = recs[i].Count
	}
	return out
}

func TestPoolExpiryReplacementInsideLookback(t *testing.T) {
	t.Parallel()
	_, _, _, _, baseline := runLookback(t, nil, defaultOpts(), nil)

	before, after, adjusted, missing, sized := runLookback(t,
		[]common.Commitment{expiringRI(2), replacementRI(2, 5)}, defaultOpts(), nil)
	assert.Zero(t, adjusted)
	assert.Zero(t, missing)
	for i := range after {
		assert.Equal(t, before[i].ExistingCoveragePct, after[i].ExistingCoveragePct)
	}
	assert.Equal(t, counts(baseline), counts(sized), "no re-buy of what was already bought")

	t.Run("lookback 0 still deducts", func(t *testing.T) {
		opts := PoolExpiryOptions{WindowDays: 30}
		before, after, adjusted, _, _ := runLookback(t,
			[]common.Commitment{expiringRI(2), replacementRI(2, 5)}, opts, nil)
		assert.Equal(t, 2, adjusted)
		assert.Less(t, after[0].ExistingCoveragePct, before[0].ExistingCoveragePct)
	})
}

func TestPoolExpiryReplacementBeforeLookback(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		ago  int
		want int
	}{{"40d ago deducts fully", 40, 2}, {"29d ago is credited", 29, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, adjusted, _, _ := runLookback(t,
				[]common.Commitment{expiringRI(2), replacementRI(2, tc.ago)}, defaultOpts(), nil)
			assert.Equal(t, tc.want, adjusted)
		})
	}
}

func TestPoolExpiryPartialAndOverReplacement(t *testing.T) {
	t.Parallel()
	// demand 30/h, coverage 60%: each expiring RI is 100/30 points.
	before, after, adjusted, _, _ := runLookback(t,
		[]common.Commitment{expiringRI(4), replacementRI(1, 5)}, defaultOpts(), nil)
	assert.Equal(t, 2, adjusted)
	assert.InDelta(t, before[0].ExistingCoveragePct-3*100.0/30, after[0].ExistingCoveragePct, 1e-9)

	before, after, adjusted, _, _ = runLookback(t,
		[]common.Commitment{expiringRI(2), replacementRI(5, 5)}, defaultOpts(), nil)
	assert.Zero(t, adjusted)
	assert.Equal(t, before[0].ExistingCoveragePct, after[0].ExistingCoveragePct, "never above lagged coverage")
}

func TestPoolExpiryReplacementStates(t *testing.T) {
	t.Parallel()
	start, end := daysFromNow(-5), daysFromNow(300)
	for _, tc := range []struct {
		name     string
		c        common.Commitment
		credited bool
	}{
		{"retired", ec2Commit(2, "retired", start, end), false},
		{"expired", ec2Commit(2, "expired", start, end), false},
		{"queued", ec2Commit(2, "queued", start, end), false},
		{"pending-return", ec2Commit(2, "pending-return", start, end), false},
		{"also expiring", ec2Commit(2, "active", start, daysFromNow(20)), false},
		{"payment-pending", ec2Commit(2, "payment-pending", start, end), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, adjusted, _, _ := runLookback(t, []common.Commitment{expiringRI(2), tc.c}, defaultOpts(), nil)
			if tc.credited {
				assert.Zero(t, adjusted)
			} else {
				assert.Equal(t, 2, adjusted)
			}
		})
	}
}

func TestPoolExpiryReplacementPoolMatching(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		mod      func(*common.Commitment)
		credited bool
	}{
		{"region mismatch", func(c *common.Commitment) { c.Region = "us-west-2" }, false},
		{"type mismatch", func(c *common.Commitment) { c.ResourceType = "m5.xlarge" }, false},
		{"service mismatch", func(c *common.Commitment) { c.Service = common.ServiceRDS }, false},
		{"account ignored", func(c *common.Commitment) { c.Account = "other-linked-account" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repl := replacementRI(2, 5)
			tc.mod(&repl)
			_, _, adjusted, _, _ := runLookback(t, []common.Commitment{expiringRI(2), repl}, defaultOpts(), nil)
			if tc.credited {
				assert.Zero(t, adjusted)
			} else {
				assert.Equal(t, 2, adjusted)
			}
		})
	}
}

func TestPoolExpiryNoExpiringCommitments(t *testing.T) {
	t.Parallel()
	before, after, adjusted, missing, _ := runLookback(t,
		[]common.Commitment{replacementRI(3, 5)}, defaultOpts(), nil)
	assert.Zero(t, adjusted)
	assert.Zero(t, missing)
	assert.Equal(t, before, after)
}

func TestPoolExpiryMissingDemand(t *testing.T) {
	t.Parallel()
	noDemand := func(cov PoolCoverageMap) {
		for k, v := range cov {
			v.AvgInstancesPerHour = 0
			cov[k] = v
		}
	}
	_, _, adjusted, missing, _ := runLookback(t,
		[]common.Commitment{expiringRI(2), replacementRI(2, 5)}, defaultOpts(), noDemand)
	assert.Zero(t, adjusted)
	assert.Zero(t, missing, "fully replaced: nothing to deduct")

	_, _, adjusted, missing, _ = runLookback(t,
		[]common.Commitment{expiringRI(3), replacementRI(1, 5)}, defaultOpts(), noDemand)
	assert.Zero(t, adjusted)
	assert.Equal(t, 2, missing)
}

func TestPoolExpirySkippedRecsKeepExactState(t *testing.T) {
	t.Parallel()
	recs, _, coverage := expiryPrecisionFixture()
	ApplyCoverageMapToRecommendations(recs, coverage)
	seeded := make([]*big.Rat, len(recs))
	for i := range recs {
		seeded[i] = big.NewRat(1, 2)
		recs[i].ExistingCoveragePercentExact = seeded[i]
	}
	adjusted, _ := AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(recs,
		[]common.Commitment{expiringRI(2), replacementRI(2, 5)}, coverage, defaultOpts())
	require.Zero(t, adjusted)
	for i := range recs {
		assert.Same(t, seeded[i], recs[i].ExistingCoveragePercentExact)
		assert.Zero(t, big.NewRat(1, 2).Cmp(recs[i].ExistingCoveragePercentExact))
	}
}

func TestPoolExpiryZeroOptionsMatchLegacyCoverageAPI(t *testing.T) {
	t.Parallel()
	commits := []common.Commitment{expiringRI(2), replacementRI(2, 5)}
	recsA, _, coverage := expiryPrecisionFixture()
	recsB, _, _ := expiryPrecisionFixture()
	ApplyCoverageMapToRecommendations(recsA, coverage)
	ApplyCoverageMapToRecommendations(recsB, coverage)
	a1, m1 := AdjustExistingCoverageForExpiringCommitmentsWithCoverage(recsA, commits, 30, coverage)
	a2, m2 := AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(recsB, commits, coverage,
		PoolExpiryOptions{WindowDays: 30})
	assert.Equal(t, a1, a2)
	assert.Equal(t, m1, m2)
	assert.Equal(t, recsA, recsB)

	a, m := AdjustExistingCoverageForExpiringCommitmentsWithCoverageOptions(recsB, commits, coverage,
		PoolExpiryOptions{CoverageLookbackDays: lookbackDays})
	assert.Zero(t, a)
	assert.Zero(t, m)
}
