package recommendations

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

const replTestLookback = 30

func replTestOpts() FamilyExpiryOptions {
	return FamilyExpiryOptions{WindowDays: 30, CoverageLookbackDays: replTestLookback}
}

// replTestStarted returns c started daysAgo days ago.
func replTestStarted(c common.Commitment, daysAgo int) common.Commitment {
	c.StartDate = time.Now().Add(-time.Duration(daysAgo) * 24 * time.Hour)
	return c
}

// replTestRepro is cli#2132 F1: lagged coverage 66 of 80 NU (82.5%), account A
// has 4 x large expiring and 4 x large replacements bought 2 days ago; 12 x
// large in other accounts are not in the inventory. True post-expiry gap is 0.
func replTestRepro() (PoolCoverageMap, []common.Recommendation, []common.Commitment) {
	cov := expiryTestCov("db.r6g.xlarge", 10, 82.5)
	recs := expiryTestRec("db.r6g.large", 10)
	commits := []common.Commitment{
		replTestStarted(expiryTestRI("db.r6g.large", "a", 4, 15), 700),
		replTestStarted(expiryTestRI("db.r6g.large", "a", 4, 700), 2),
	}
	return cov, recs, commits
}

func TestExpiryReplacements_Regression(t *testing.T) {
	t.Parallel()
	t.Run("repro: own recent replacements are credited, nothing is bought", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		sized, _, _, _ := ApplyFamilyNUSizingRDSWithExpiry(recs, cov, 80, commits, 30)
		require.Len(t, sized, 1, "current function over-buys")
		assert.Equal(t, 3, sized[0].Count)

		sized, _, drops, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		assert.Empty(t, sized)
		assert.Equal(t, 1, drops.AlreadyAtTarget)
		assert.Zero(t, rep.ExpiringNU, "replacements absorb the whole expiry: covered NU is unchanged")
		assert.Equal(t, 16.0, rep.ReplacementNU)
	})

	t.Run("full inventory still buys nothing", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		commits = append(commits, replTestStarted(expiryTestRI("db.r6g.large", "b", 12, 700), 700))
		sized, _, drops, _ := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		assert.Empty(t, sized)
		assert.Equal(t, 1, drops.AlreadyAtTarget)
	})

	t.Run("go#341 F1 case: unlisted account RIs, no replacements, still buys 2", func(t *testing.T) {
		cov, recs, _ := expiryTestScenario()
		commits := []common.Commitment{replTestStarted(expiryTestRI("db.r6g.large", "a", 4, 15), 700)}
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		require.Len(t, sized, 1)
		assert.Equal(t, 2, sized[0].Count)
		assert.Zero(t, rep.ReplacementNU)
	})

	t.Run("replacements started outside the lookback are not counted", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		commits[1] = replTestStarted(commits[1], replTestLookback+10)
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		require.Len(t, sized, 1)
		assert.Equal(t, 3, sized[0].Count)
		assert.Zero(t, rep.ReplacementNU)
	})

	t.Run("replacements that are themselves expiring are not counted", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		commits[1] = replTestStarted(expiryTestRI("db.r6g.large", "a", 4, 20), 2)
		_, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		assert.Zero(t, rep.ReplacementNU)
		assert.Equal(t, 32.0, rep.ExpiringNU)
	})

	t.Run("inactive and other-family replacements are not counted", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		retired := replTestStarted(expiryTestRI("db.r6g.large", "a", 4, 700), 2)
		retired.State = "retired"
		otherFamily := replTestStarted(expiryTestRI("db.r7g.large", "a", 4, 700), 2)
		commits[1] = retired
		commits = append(commits, otherFamily)
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		require.Len(t, sized, 1)
		assert.Equal(t, 3, sized[0].Count)
		assert.Zero(t, rep.ReplacementNU)
	})

	t.Run("mixed sizes are summed in NU", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		commits[1] = replTestStarted(expiryTestRI("db.r6g.xlarge", "a", 1, 700), 2)
		commits = append(commits, replTestStarted(expiryTestRI("db.r6g.large", "a", 2, 700), 5))
		_, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits, replTestOpts())
		assert.Equal(t, 16.0, rep.ReplacementNU, "8 + 2x4 NU")
	})

	t.Run("lookback 0 credits nothing", func(t *testing.T) {
		cov, recs, commits := replTestRepro()
		sized, _, _, rep := ApplyFamilyNUSizingRDSWithExpiryOptions(recs, cov, 80, commits,
			FamilyExpiryOptions{WindowDays: 30})
		require.Len(t, sized, 1)
		assert.Equal(t, 3, sized[0].Count)
		assert.Zero(t, rep.ReplacementNU)
	})
}

// legacyAdjust is the merged (#341) per-family formula, kept verbatim as the
// reference for the R = 0 equivalence test.
func legacyAdjust(c, exp, active float64) float64 {
	return math.Max(0, math.Max(c-exp, math.Min(c, active-exp)))
}

type replWorld struct {
	own, all []common.Commitment
	cov      PoolCoverageMap
	c, t     float64
}

var replSizes = []string{"db.r6g.large", "db.r6g.xlarge", "db.r6g.2xlarge"}

// replGenWorld builds a random family. Own account: expiring, long-lived and
// recent (replacement) RIs. Other accounts: old RIs, some expiring, invisible
// to the inventory. Lagged coverage C counts every old RI in full plus a
// random fraction f of the recent ones; usage exceeds every RI, so coverage is
// not saturated.
func replGenWorld(rng *rand.Rand) replWorld {
	var w replWorld
	old, recent := 0.0, 0.0
	add := func(visible bool, endDays, startDaysAgo int, isRecent bool) {
		size := replSizes[rng.Intn(len(replSizes))]
		cnt := 1 + rng.Intn(4)
		c := replTestStarted(expiryTestRI(size, "x", cnt, endDays), startDaysAgo)
		nu := float64(cnt) * rdsInstanceNUFromType(size)
		if isRecent {
			recent += nu
		} else {
			old += nu
		}
		if visible {
			w.own = append(w.own, c)
		}
		w.all = append(w.all, c)
	}
	for i := rng.Intn(4); i > 0; i-- {
		add(true, 1+rng.Intn(29), 100+rng.Intn(600), false)
	}
	for i := rng.Intn(4); i > 0; i-- {
		add(true, 100+rng.Intn(600), 100+rng.Intn(600), false)
	}
	for i := rng.Intn(4); i > 0; i-- {
		add(true, 100+rng.Intn(600), rng.Intn(replTestLookback), true)
	}
	for i := rng.Intn(5); i > 0; i-- {
		end := 100 + rng.Intn(600)
		if rng.Intn(3) == 0 {
			end = 1 + rng.Intn(29)
		}
		add(false, end, 100+rng.Intn(600), false)
	}
	w.c = old + rng.Float64()*recent
	w.t = old + recent + 1 + rng.Float64()*40
	w.cov = expiryTestCov("db.r6g.large", w.t/4, w.c/w.t*100)
	return w
}

func replFamilyCovered(t *testing.T, w replWorld, commits []common.Commitment, opts FamilyExpiryOptions) (float64, FamilyExpiryReport) {
	t.Helper()
	fcs := AggregateRDSFamilyCoverage(w.cov)
	require.Len(t, fcs, 1)
	rep := adjustFamilyCoverageForExpiry(fcs, commits, opts, time.Now())
	for _, fc := range fcs {
		return fc.CoveredNU, rep
	}
	return 0, rep
}

func replBought(w replWorld, covered float64) int {
	fcs := AggregateRDSFamilyCoverage(w.cov)
	for k, fc := range fcs {
		fc.CoveredNU = covered
		fcs[k] = fc
	}
	sized, _, _ := sizeRDSFamilies(expiryTestRec("db.r6g.xlarge", 40), fcs, 90)
	if len(sized) == 0 {
		return 0
	}
	return sized[0].Count
}

func TestExpiryReplacements_ZeroLookbackMatchesMergedFormula(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(341))
	for seed := 0; seed < 3000; seed++ {
		w := replGenWorld(rng)
		got, rep := replFamilyCovered(t, w, w.own, FamilyExpiryOptions{WindowDays: 30})
		fc := AggregateRDSFamilyCoverage(w.cov)
		var base FamilyCoverage
		for _, v := range fc {
			base = v
		}
		exp, active := 0.0, 0.0
		for _, c := range w.own {
			nu := float64(c.Count) * rdsInstanceNUFromType(c.ResourceType)
			active += nu
			if c.EndDate.Before(time.Now().Add(30 * 24 * time.Hour)) {
				exp += nu
			}
		}
		want := base.CoveredNU
		if exp > 0 {
			want = legacyAdjust(base.CoveredNU, exp, active)
		}
		require.InDelta(t, want, got, 1e-9, "seed %d", seed)
		require.Zero(t, rep.ReplacementNU)
	}
}

func TestExpiryReplacements_Properties(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewSource(2132))
	creditedWorlds := 0
	for seed := 0; seed < 3000; seed++ {
		w := replGenWorld(rng)
		base, _ := replFamilyCovered(t, w, nil, replTestOpts())
		old, _ := replFamilyCovered(t, w, w.own, FamilyExpiryOptions{WindowDays: 30})
		got, rep := replFamilyCovered(t, w, w.own, replTestOpts())
		oracle, _ := replFamilyCovered(t, w, w.all, FamilyExpiryOptions{WindowDays: 30})

		require.GreaterOrEqual(t, got, old-1e-9, "seed %d: never below the R=0 formula", seed)
		require.LessOrEqual(t, got, base+1e-9, "seed %d: never above measured coverage", seed)
		require.LessOrEqual(t, base-got, rep.ExpiringNU+1e-9, "seed %d: deduction never exceeds expiring NU", seed)
		require.GreaterOrEqual(t, got, oracle-1e-9, "seed %d: never below the full-inventory covered NU", seed)
		require.LessOrEqual(t, replBought(w, got), replBought(w, oracle), "seed %d: purchase never exceeds the full-inventory gap", seed)
		require.LessOrEqual(t, replBought(w, got), replBought(w, old), "seed %d: purchase never exceeds the R=0 purchase", seed)
		if rep.ReplacementNU > 0 && got > old {
			creditedWorlds++
		}
	}
	assert.Greater(t, creditedWorlds, 100, "generator must exercise the credit")
}
