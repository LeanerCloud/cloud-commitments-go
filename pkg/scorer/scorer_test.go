package scorer

import (
	"math"
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
)

func rec(service common.ServiceType, region, resourceType string, savingsPct, savings, cost float64, count int, breakEven float64) common.Recommendation {
	return common.Recommendation{
		Service:           service,
		Region:            region,
		ResourceType:      resourceType,
		SavingsPercentage: savingsPct,
		EstimatedSavings:  savings,
		CommitmentCost:    cost,
		Count:             count,
		BreakEvenMonths:   breakEven,
	}
}

func TestScore_NoFilters(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 30.0, 1000.0, 3000.0, 5, 0),
		rec(common.ServiceRDS, "eu-west-1", "db.r5.large", 20.0, 500.0, 2500.0, 2, 0),
	}
	result := Score(recs, Config{})
	assert.Len(t, result.Passed, 2)
	assert.Empty(t, result.Filtered)
}

func TestScore_MinSavingsPct(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 3.2, 100.0, 3000.0, 1, 0),
		rec(common.ServiceRDS, "us-east-1", "db.r5.large", 20.0, 500.0, 2500.0, 1, 0),
	}
	result := Score(recs, Config{MinSavingsPct: 5.0})
	assert.Len(t, result.Passed, 1)
	assert.Equal(t, common.ServiceRDS, result.Passed[0].Service)
	assert.Len(t, result.Filtered, 1)
	assert.Contains(t, result.Filtered[0].FilterReason, "3.2%")
	assert.Contains(t, result.Filtered[0].FilterReason, "5.0%")
}

func TestScore_MaxBreakEvenMonths(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 10.0, 500.0, 5000.0, 1, 18.0),
		rec(common.ServiceEC2, "us-east-1", "c5.large", 15.0, 750.0, 5000.0, 1, 6.0),
	}
	result := Score(recs, Config{MaxBreakEvenMonths: 12})
	assert.Len(t, result.Passed, 1)
	assert.Equal(t, "c5.large", result.Passed[0].ResourceType)
	assert.Len(t, result.Filtered, 1)
	assert.Contains(t, result.Filtered[0].FilterReason, "18.0 months")
}

func TestScore_MinCount(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 10.0, 100.0, 1000.0, 2, 0),
		rec(common.ServiceEC2, "us-east-1", "c5.large", 10.0, 200.0, 2000.0, 5, 0),
	}
	result := Score(recs, Config{MinCount: 3})
	assert.Len(t, result.Passed, 1)
	assert.Equal(t, "c5.large", result.Passed[0].ResourceType)
	assert.Contains(t, result.Filtered[0].FilterReason, "count 2")
}

func TestScore_EnabledServices(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 10.0, 500.0, 5000.0, 1, 0),
		rec(common.ServiceRDS, "us-east-1", "db.r5.large", 20.0, 300.0, 1500.0, 1, 0),
		rec(common.ServiceElastiCache, "us-east-1", "cache.r6g.large", 15.0, 200.0, 1000.0, 1, 0),
	}
	result := Score(recs, Config{EnabledServices: []string{"ec2", "rds"}})
	assert.Len(t, result.Passed, 2)
	assert.Len(t, result.Filtered, 1)
	assert.Equal(t, common.ServiceElastiCache, result.Filtered[0].Recommendation.Service)
}

func TestScore_ZeroThresholds_NoFilter(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 0.1, 1.0, 1000.0, 1, 100.0),
	}
	result := Score(recs, Config{MinSavingsPct: 0, MaxBreakEvenMonths: 0, MinCount: 0})
	assert.Len(t, result.Passed, 1)
	assert.Empty(t, result.Filtered)
}

func TestScore_Sorted(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 10.0, 500.0, 5000.0, 1, 0),
		rec(common.ServiceRDS, "us-east-1", "db.r5.large", 30.0, 300.0, 1000.0, 1, 0),
		rec(common.ServiceEC2, "eu-west-1", "c5.large", 20.0, 700.0, 3000.0, 1, 0),
	}
	result := Score(recs, Config{})
	assert.Equal(t, 30.0, result.Passed[0].SavingsPercentage)
	assert.Equal(t, 20.0, result.Passed[1].SavingsPercentage)
	assert.Equal(t, 10.0, result.Passed[2].SavingsPercentage)
}

func TestScore_SortedTieBreak(t *testing.T) {
	t.Parallel()
	// Equal SavingsPercentage: sort by EstimatedSavings desc
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 20.0, 500.0, 2500.0, 1, 0),
		rec(common.ServiceEC2, "us-east-1", "c5.large", 20.0, 700.0, 3500.0, 1, 0),
	}
	result := Score(recs, Config{})
	assert.Equal(t, "c5.large", result.Passed[0].ResourceType, "higher savings first")
}

func TestScore_SortedDeterministicTieBreak(t *testing.T) {
	t.Parallel()
	// Equal SavingsPercentage and EstimatedSavings: sort by Service+Region+ResourceType asc
	recs := []common.Recommendation{
		rec(common.ServiceRDS, "us-east-1", "db.z", 20.0, 500.0, 2500.0, 1, 0),
		rec(common.ServiceEC2, "us-east-1", "m5.large", 20.0, 500.0, 2500.0, 1, 0),
	}
	result := Score(recs, Config{})
	assert.Equal(t, common.ServiceEC2, result.Passed[0].Service, "ec2 < rds alphabetically")
}

func TestScore_CombinedFilters(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "m5.large", 3.0, 100.0, 3000.0, 1, 0),     // fails MinSavingsPct
		rec(common.ServiceEC2, "us-east-1", "c5.large", 20.0, 500.0, 2500.0, 1, 0),    // passes all
		rec(common.ServiceRDS, "eu-west-1", "db.r5.large", 20.0, 200.0, 1000.0, 1, 0), // fails EnabledServices
	}
	result := Score(recs, Config{MinSavingsPct: 5.0, EnabledServices: []string{"ec2"}})
	assert.Len(t, result.Passed, 1)
	assert.Equal(t, "c5.large", result.Passed[0].ResourceType)
	assert.Len(t, result.Filtered, 2)
}

// Live shape: EC2 partial-upfront recs from the AWS parser never carry
// BreakEvenMonths, so the filter must derive it from upfront, savings and term.
func TestScore_MaxBreakEvenMonths_ComputedFromCosts(t *testing.T) {
	t.Parallel()
	partial := rec(common.ServiceEC2, "us-east-1", "m5.large", 30, 40, 720, 1, 0)
	partial.Term = "1yr"
	noUpfront := rec(common.ServiceEC2, "us-east-1", "c5.large", 20, 25, 0, 1, 0)
	noUpfront.Term = "1yr"
	unknownTerm := rec(common.ServiceEC2, "us-east-1", "r5.large", 20, 25, 500, 1, 0)

	result := Score([]common.Recommendation{partial, noUpfront, unknownTerm}, Config{MaxBreakEvenMonths: 1})

	assert.Len(t, result.Passed, 1)
	assert.Equal(t, "c5.large", result.Passed[0].ResourceType, "no-upfront has nothing to recover")
	assert.Len(t, result.Filtered, 2)
	byType := map[string]string{}
	for _, f := range result.Filtered {
		byType[f.Recommendation.ResourceType] = f.FilterReason
	}
	assert.Contains(t, byType["m5.large"], "7.2 months exceeds maximum 1")
	assert.Contains(t, byType["r5.large"], "cannot be computed")

	loose := Score([]common.Recommendation{partial}, Config{MaxBreakEvenMonths: 12})
	assert.Len(t, loose.Passed, 1)
	assert.InDelta(t, 7.2, loose.Passed[0].BreakEvenMonths, 1e-9, "computed value is surfaced for reports")
}

// #392: Azure Advisor fans each recommendation out to an "upfront" and a
// "monthly" variant, both with CommitmentCost 0 because Advisor reports no
// price. The upfront variant's break-even is unknown, not 0 months.
func TestScore_MaxBreakEvenMonths_PriceUnknown(t *testing.T) {
	t.Parallel()
	upfront := rec(common.ServiceCompute, "eastus", "Standard_D2s_v3", 0, 50, 0, 1, 0)
	upfront.Term, upfront.PaymentOption = "1yr", "upfront"
	monthly := upfront
	monthly.PaymentOption = "monthly"
	awsNoUpfront := rec(common.ServiceEC2, "us-east-1", "m5.large", 30, 40, 0, 1, 0)
	awsNoUpfront.Term, awsNoUpfront.PaymentOption = "1yr", "no-upfront"
	awsPartialNoPrice := awsNoUpfront
	awsPartialNoPrice.ResourceType, awsPartialNoPrice.PaymentOption = "c5.large", "partial-upfront"
	providerValue := awsPartialNoPrice
	providerValue.ResourceType, providerValue.BreakEvenMonths = "r5.large", 6
	recs := []common.Recommendation{upfront, monthly, awsNoUpfront, awsPartialNoPrice, providerValue}

	result := Score(recs, Config{MaxBreakEvenMonths: 12})

	passed := map[string]bool{}
	for _, p := range result.Passed {
		passed[p.ResourceType+"|"+p.PaymentOption] = true
	}
	assert.Equal(t, map[string]bool{
		"Standard_D2s_v3|monthly":  true,
		"m5.large|no-upfront":      true,
		"r5.large|partial-upfront": true,
	}, passed, "known zero upfront and a provider break-even still pass")
	assert.Len(t, result.Filtered, 2)
	for _, f := range result.Filtered {
		assert.True(t, strings.HasPrefix(f.FilterReason, FilterReasonPriceUnknown+":"), f.FilterReason)
		assert.Contains(t, f.FilterReason, f.Recommendation.PaymentOption)
	}

	unfiltered := Score(recs, Config{})
	assert.Len(t, unfiltered.Passed, len(recs), "price-unknown only drops recs when break-even filtering is active")
	for _, p := range unfiltered.Passed {
		if p.ResourceType != "r5.large" {
			assert.Zero(t, p.BreakEvenMonths, "unknown or zero break-even is never surfaced")
		}
	}
}

func TestScore_MinSavingsPctSet(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "neg", -12.0, -100.0, 3000.0, 1, 0),
		rec(common.ServiceEC2, "us-east-1", "zero", 0.0, 0.0, 3000.0, 1, 0),
		rec(common.ServiceEC2, "us-east-1", "low", 3.2, 100.0, 3000.0, 1, 0),
		rec(common.ServiceRDS, "us-east-1", "high", 20.0, 500.0, 2500.0, 1, 0),
	}
	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{"unset zero is no filter", Config{}, []string{"high", "low", "zero", "neg"}},
		{"set to 0 is a floor at 0", Config{MinSavingsPctSet: true}, []string{"high", "low", "zero"}},
		{"set to 5 filters at 5", Config{MinSavingsPct: 5, MinSavingsPctSet: true}, []string{"high"}},
		{"positive without set is unchanged", Config{MinSavingsPct: 5}, []string{"high"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, r := range Score(recs, tt.cfg).Passed {
				got = append(got, r.ResourceType)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()
	for _, v := range []float64{0, 5, 100} {
		assert.NoError(t, Config{MinSavingsPct: v, MinSavingsPctSet: true}.Validate(), "%v", v)
	}
	const negative, nonFinite = "min_savings_pct must be ≥ 0", "min_savings_pct must be a finite number"
	for v, want := range map[float64]string{
		-1: negative, math.NaN(): nonFinite, math.Inf(1): nonFinite, math.Inf(-1): nonFinite,
	} {
		assert.EqualError(t, Config{MinSavingsPct: v, MinSavingsPctSet: true}.Validate(), want, "%v", v)
	}
}

func TestScore_MinSavingsPct_NonFiniteFailsClosed(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		rec(common.ServiceEC2, "us-east-1", "a", 30.0, 100.0, 3000.0, 1, 0),
		rec(common.ServiceRDS, "us-east-1", "b", 99.0, 500.0, 2500.0, 1, 0),
	}
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		result := Score(recs, Config{MinSavingsPct: v, MinSavingsPctSet: true})
		assert.Empty(t, result.Passed, "%v", v)
		assert.Len(t, result.Filtered, 2, "%v", v)
	}
}
