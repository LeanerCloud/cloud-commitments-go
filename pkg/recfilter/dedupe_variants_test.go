package recfilter

import (
	"context"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func variantRec(term, payment string) common.Recommendation {
	return common.Recommendation{ResourceType: "Premium_P1", Region: "eastus", Count: 2, Term: common.Term(term), PaymentOption: common.PaymentOption(payment)}
}

func adjustCounts(t *testing.T, held []common.Commitment, recs []common.Recommendation) []int {
	t.Helper()
	d := NewDuplicateChecker(0)
	passed, _, err := d.AdjustRecommendationsForExisting(context.Background(), recs, &fakeServiceClient{commitments: held})
	require.NoError(t, err)
	counts := make([]int, len(passed))
	for i, r := range passed {
		counts[i] = r.Count
	}
	return counts
}

func heldOne() []common.Commitment {
	return []common.Commitment{{ResourceType: "Premium_P1", Region: "eastus", Count: 1, State: "active", StartDate: time.Now().Add(-time.Hour)}}
}

// #703: term and payment variants are alternatives for the same resource, so
// each variant sees the full held quantity.
func TestDedupe_VariantsEachSeeFullHeldQuantity(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		variantRec("1yr", "upfront"), variantRec("1yr", "monthly"),
		variantRec("3yr", "upfront"), variantRec("3yr", "monthly"),
	}
	assert.Equal(t, []int{1, 1, 1, 1}, adjustCounts(t, heldOne(), recs))
}

func TestDedupe_SameVariantStillSharesBudgetInOrder(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		variantRec("1yr", "upfront"), variantRec("1yr", "upfront"),
		variantRec("3yr", "upfront"),
	}
	assert.Equal(t, []int{1, 2, 1}, adjustCounts(t, heldOne(), recs))
}

func TestDedupe_VariantsWithDifferentKeysUnaffected(t *testing.T) {
	t.Parallel()
	other := variantRec("1yr", "monthly")
	other.ResourceType = "Premium_P2"
	recs := []common.Recommendation{variantRec("1yr", "upfront"), other}
	assert.Equal(t, []int{1, 2}, adjustCounts(t, heldOne(), recs))
}

// An empty term or payment is its own group: it is not folded into a named
// variant, because an unknown variant cannot be assumed to be the same offer.
func TestDedupe_EmptyTermOrPaymentIsOwnGroup(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{
		variantRec("", ""), variantRec("", ""),
		variantRec("1yr", ""), variantRec("", "upfront"),
	}
	assert.Equal(t, []int{1, 2, 1, 1}, adjustCounts(t, heldOne(), recs))
}

func TestDedupe_ElastiCacheCoverWithVariants(t *testing.T) {
	t.Parallel()
	held := []common.Commitment{{
		Provider: common.ProviderAWS, Service: common.ServiceElastiCache, ResourceType: "cache.r6g.large",
		Region: "us-east-1", Engine: "redis", Count: 4, State: "active", StartDate: time.Now().Add(-time.Hour),
	}}
	mk := func(term string) common.Recommendation {
		return common.Recommendation{
			Provider: common.ProviderAWS, Service: common.ServiceElastiCache, ResourceType: "cache.r6g.large",
			Region: "us-east-1", Count: 6, Term: common.Term(term), PaymentOption: "no-upfront",
			Details: &common.CacheDetails{Engine: "valkey"},
		}
	}
	// 4 Redis nodes cover 5 Valkey nodes, once per variant: 6 -> 1, 1.
	assert.Equal(t, []int{1, 1}, adjustCounts(t, held, []common.Recommendation{mk("1yr"), mk("3yr")}))
}

func TestDedupe_EC2VariantsEachSeeFullHeldQuantity(t *testing.T) {
	t.Parallel()
	held := []common.Commitment{{
		Provider: common.ProviderAWS, Service: common.ServiceEC2, ResourceType: "m5.large", Region: "us-east-1",
		Platform: "Linux/UNIX", Tenancy: "default", Scope: "Region", Count: 1, State: "active", StartDate: time.Now().Add(-time.Hour),
	}}
	mk := func(term, payment string) common.Recommendation {
		return common.Recommendation{
			Provider: common.ProviderAWS, Service: common.ServiceEC2, ResourceType: "m5.large", Region: "us-east-1",
			Count: 2, Term: common.Term(term), PaymentOption: common.PaymentOption(payment),
			Details: &common.ComputeDetails{Platform: "Linux/UNIX", Tenancy: "default", Scope: "region"},
		}
	}
	got := adjustCounts(t, held, []common.Recommendation{
		mk("1yr", "all-upfront"), mk("1yr", "no-upfront"), mk("3yr", "all-upfront"), mk("1yr", "all-upfront"),
	})
	assert.Equal(t, []int{1, 1, 1, 2}, got)
}

func TestDedupe_VariantNormalization(t *testing.T) {
	t.Parallel()
	recs := []common.Recommendation{variantRec("1yr", "upfront"), variantRec(" 1YR ", " UPFRONT ")}
	assert.Equal(t, []int{1, 2}, adjustCounts(t, heldOne(), recs), "same variant after trim and case folding shares the budget")
	recs = []common.Recommendation{variantRec("1yr", "upfront"), variantRec(" 1YR ", "monthly")}
	assert.Equal(t, []int{1, 1}, adjustCounts(t, heldOne(), recs), "term folds case, payment differs")
	recs = []common.Recommendation{variantRec("1yr", "monthly"), variantRec("1yr", " MONTHLY ")}
	assert.Equal(t, []int{1, 2}, adjustCounts(t, heldOne(), recs), "payment folds case")
}
