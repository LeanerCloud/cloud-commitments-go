package computeengine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func newRec(machineType string, vcpus int, memGB float64) common.Recommendation {
	return common.Recommendation{ResourceType: machineType, Count: vcpus, Details: common.ComputeDetails{MemoryGB: memGB}}
}

var americas = []string{"us-central1", "us-east1", "us-west1"}

// catalogSKU builds a SKU shaped like the live Cloud Billing catalog entries
// (services/6F81-5844-456A, fetched 2026-10-13); descriptions, groups, units
// and prices are copied from real SKUs.
func catalogSKU(id, description, group, usageType, unit string, nanos int64, regions []string) *cloudbilling.Sku {
	return &cloudbilling.Sku{
		SkuId:          id,
		Description:    description,
		Category:       &cloudbilling.Category{ResourceFamily: "Compute", ResourceGroup: group, UsageType: usageType},
		ServiceRegions: regions,
		PricingInfo: []*cloudbilling.PricingInfo{{PricingExpression: &cloudbilling.PricingExpression{
			UsageUnit:   unit,
			TieredRates: []*cloudbilling.TierRate{{UnitPrice: &cloudbilling.Money{CurrencyCode: "USD", Nanos: nanos}}},
		}}},
	}
}

// n1Americas1YrSKUs: N1 on-demand core and RAM, then 1-year commitment core and RAM.
func n1Americas1YrSKUs() []*cloudbilling.Sku {
	return []*cloudbilling.Sku{
		catalogSKU("2E27-4F75-95CD", "N1 Predefined Instance Core running in Americas", "N1Standard", "OnDemand", "h", 31611000, americas),
		catalogSKU("6C71-E844-38BC", "N1 Predefined Instance Ram running in Americas", "N1Standard", "OnDemand", "GiBy.h", 4237000, americas),
		catalogSKU("6FDA-DB41-BB57", "Commitment v1: Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 19915000, americas),
		catalogSKU("E35D-222F-E443", "Commitment v1: Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 2669000, americas),
	}
}

// n2AmericasSKUs: live N2 SKUs for us-central1 (Americas), plus decoys the real
// catalog carries next to them.
func n2AmericasSKUs() []*cloudbilling.Sku {
	return []*cloudbilling.Sku{
		catalogSKU("n2-od-core", "N2 Instance Core running in Americas", "CPU", "OnDemand", "h", 31611000, americas),
		catalogSKU("n2-od-ram", "N2 Instance Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 4237000, americas),
		catalogSKU("n2-c1-core", "Commitment v1: N2 Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 19915000, americas),
		catalogSKU("n2-c1-ram", "Commitment v1: N2 Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 2669000, americas),
		catalogSKU("n2-c3-core", "Commitment v1: N2 Cpu in Americas for 3 Year", "CPU", "Commit3Yr", "h", 14225000, americas),
		catalogSKU("n2-c3-ram", "Commitment v1: N2 Ram in Americas for 3 Year", "RAM", "Commit3Yr", "GiBy.h", 1907000, americas),
		// Same family name prefix, different product: must not be read as N2.
		catalogSKU("n2d-od-core", "N2D AMD Instance Core running in Americas", "CPU", "OnDemand", "h", 27502000, americas),
		catalogSKU("n2-custom-core", "N2 Custom Instance Core running in Americas", "CPU", "OnDemand", "h", 33000000, americas),
		catalogSKU("n2-sole-core", "Sole Tenancy Premium for N2 Sole Tenancy Instance Core running in Americas", "CPU", "OnDemand", "h", 1000000, americas),
		// Other region.
		catalogSKU("n2-od-core-eu", "N2 Instance Core running in Frankfurt", "CPU", "OnDemand", "h", 99000000, []string{"europe-west3"}),
	}
}

// Live catalog check, n2-standard-8 in us-central1 (8 vCPU, 32 GiB), 1 year:
//
//	on demand  8*0.031611 + 32*0.004237 = 0.388472 USD/h
//	commitment 8*0.019915 + 32*0.002669 = 0.244728 USD/h  (37.0% off)
func TestGetComputePricing_N2StandardFromLiveShapedCatalog(t *testing.T) {
	ctx := context.Background()
	client, _ := NewClient(ctx, "p", "us-central1")
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: n2AmericasSKUs()}})
	res := computeResources{vcpus: 8, memoryGiB: 32}

	one, err := client.getComputePricing(ctx, "n2-standard-8", "us-central1", 1, res)
	require.NoError(t, err)
	assert.Equal(t, "USD", one.Currency)
	assert.InDelta(t, 0.244728, one.HourlyRate, 1e-9)
	assert.InDelta(t, 0.244728*8760, one.CommitmentPrice, 1e-6)
	assert.InDelta(t, 0.388472*8760, one.OnDemandPrice, 1e-6)
	assert.InDelta(t, 37.0, one.SavingsPercentage, 0.05)

	three, err := client.getComputePricing(ctx, "n2-standard-8", "us-central1", 3, res)
	require.NoError(t, err)
	assert.InDelta(t, (8*0.014225+32*0.001907)*8760*3, three.CommitmentPrice, 1e-6)
	assert.InDelta(t, 0.388472*8760*3, three.OnDemandPrice, 1e-6)
	assert.Greater(t, three.SavingsPercentage, one.SavingsPercentage)
}

func TestFindFamilyRates_ErrorsAreLoud(t *testing.T) {
	t.Run("region without SKUs", func(t *testing.T) {
		_, err := findFamilyRates(n2AmericasSKUs(), "n2-standard-8", "asia-east1", 1)
		require.ErrorContains(t, err, "no on-demand core and RAM pricing")
	})
	t.Run("family without commitment SKUs", func(t *testing.T) {
		_, err := findFamilyRates(n2AmericasSKUs()[:2], "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "no commitment pricing")
	})
	t.Run("unit mismatch", func(t *testing.T) {
		skus := n2AmericasSKUs()
		skus[0].PricingInfo[0].PricingExpression.UsageUnit = "mo"
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "expected \"h\"")
	})
	t.Run("conflicting duplicate", func(t *testing.T) {
		skus := append(n2AmericasSKUs(), catalogSKU("dup", "N2 Instance Core running in Americas", "CPU", "OnDemand", "h", 1, americas))
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "ambiguous catalog")
	})
	t.Run("usage type disagrees with description", func(t *testing.T) {
		skus := n2AmericasSKUs()
		skus[2].Category.UsageType = "Commit3Yr"
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "usage type")
	})
	t.Run("missing currency", func(t *testing.T) {
		skus := n2AmericasSKUs()
		skus[0].PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice.CurrencyCode = ""
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "no currency")
	})
}

func TestResourcesFromRecommendation_RequiresCountAndMemory(t *testing.T) {
	_, err := resourcesFromRecommendation(newRec("n2-standard-8", 0, 32))
	require.Error(t, err)
	_, err = resourcesFromRecommendation(newRec("n2-standard-8", 8, 0))
	require.Error(t, err)
}

// Monthly figures for n2-standard-8 (8 vCPU, 32 GiB), live-shaped rates, per term.
// A division by 12 instead of termYears*12 would triple the 3-year amounts.
func TestEnrichRecWithPricing_MonthlyFiguresPerTerm(t *testing.T) {
	cases := map[string]struct {
		term                   string
		commitHourly, odHourly float64
	}{
		"1yr": {"1yr", 8*0.019915 + 32*0.002669, 8*0.031611 + 32*0.004237},
		"3yr": {"3yr", 8*0.014225 + 32*0.001907, 8*0.031611 + 32*0.004237},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			client, _ := NewClient(ctx, "p", "us-central1")
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: n2AmericasSKUs()}})
			rec := newRec("n2-standard-8", 8, 32)
			rec.Term = tc.term

			client.enrichRecWithPricing(ctx, &rec)

			assert.Zero(t, rec.CommitmentCost)
			assert.Equal(t, "USD", rec.CommitmentCurrency)
			require.NotNil(t, rec.RecurringMonthlyCost)
			assert.InDelta(t, tc.commitHourly*730, *rec.RecurringMonthlyCost, 1e-6)
			assert.InDelta(t, tc.odHourly*730, rec.OnDemandCost, 1e-6, "OnDemandCost is the monthly on-demand price")
		})
	}
}

func TestEnrichRecWithPricing_InvalidCurrency(t *testing.T) {
	for _, currency := range []string{"", "EUR"} {
		t.Run("commitment currency "+currency, func(t *testing.T) {
			ctx := context.Background()
			client, err := NewClient(ctx, "test-project", "us-central1")
			require.NoError(t, err)
			skus := n2AmericasSKUs()
			skus[2].PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice.CurrencyCode = currency
			client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: skus}})
			rec := newRec("n2-standard-4", 4, 16)
			rec.Term = "1yr"
			client.enrichRecWithPricing(ctx, &rec)
			assert.Empty(t, rec.CommitmentCurrency)
			assert.Nil(t, rec.RecurringMonthlyCost)
		})
	}
}

func TestClassify_LegacyCommitmentNames(t *testing.T) {
	cases := []struct {
		desc, machine, usage string
		term                 int
		matched, isRAM       bool
	}{
		{"Commitment: Compute optimized Core running in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, true, false},
		{"Commitment: Compute optimized Ram running in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, true, true},
		{"Commitment: Compute optimized Core running in Americas for 3 Year", "c2-standard-8", "Commit3Yr", 3, true, false},
		{"Commitment: Compute optimized Ram running in Americas for 3 Year", "c2-standard-8", "Commit3Yr", 3, true, true},
		// Wrong term for the requested one: skipped, not an error.
		{"Commitment: Compute optimized Core running in Americas for 3 Year", "c2-standard-8", "Commit3Yr", 1, false, false},
		// Families off the legacy allowlist never match the legacy form.
		{"Commitment: N2 Core running in Americas for 1 Year", "n2-standard-8", "Commit1Yr", 1, false, false},
		{"Commitment: Compute optimized Core running in Americas for 1 Year", "n2-standard-8", "Commit1Yr", 1, false, false},
		{"Commitment: Memory-optimized Core running in Americas for 1 Year", "m1-megamem-96", "Commit1Yr", 1, false, false},
		// c2 machine, other family labels or a longer label: anchoring must reject them.
		{"Commitment: C2D AMD Core running in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, false, false},
		{"Commitment: Memory-optimized Core running in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, false, false},
		{"Commitment: Compute optimized-X Ram running in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, false, false},
		// Existing v1 form still resolves for c2.
		{"Commitment v1: Compute optimized Cpu in Americas for 1 Year", "c2-standard-8", "Commit1Yr", 1, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.desc+"/"+tc.machine, func(t *testing.T) {
			labels, err := familyLabels(tc.machine)
			require.NoError(t, err)
			sku := catalogSKU("x", tc.desc, "CPU", tc.usage, "h", 1, americas)
			_, isRAM, matched, err := newRateMatcher(labels, "us-central1", tc.term).classify(sku)
			require.NoError(t, err)
			assert.Equal(t, tc.matched, matched)
			assert.Equal(t, tc.isRAM, isRAM)
		})
	}
}

func TestFindFamilyRates_C2LegacyCommitmentCatalog(t *testing.T) {
	skus := []*cloudbilling.Sku{
		catalogSKU("c2-od-core", "Compute optimized Core running in Americas", "CPU", "OnDemand", "h", 33174000, americas),
		catalogSKU("c2-od-ram", "Compute optimized Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 4446000, americas),
		catalogSKU("c2-l1-core", "Commitment: Compute optimized Core running in Americas for 1 Year", "CPU", "Commit1Yr", "h", 20900000, americas),
		catalogSKU("c2-l1-ram", "Commitment: Compute optimized Ram running in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 2800000, americas),
	}
	rates, err := findFamilyRates(skus, "c2-standard-8", "us-central1", 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.0209, rates.commitment.cpu, 1e-9)
	assert.InDelta(t, 0.0028, rates.commitment.ram, 1e-9)
}

func TestFindFamilyRates_C2V1AndLegacyDisagreeIsAmbiguous(t *testing.T) {
	skus := []*cloudbilling.Sku{
		catalogSKU("c2-od-core", "Compute optimized Core running in Americas", "CPU", "OnDemand", "h", 33174000, americas),
		catalogSKU("c2-od-ram", "Compute optimized Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 4446000, americas),
		catalogSKU("c2-v1-core", "Commitment v1: Compute optimized Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 20900000, americas),
		catalogSKU("c2-l1-core", "Commitment: Compute optimized Core running in Americas for 1 Year", "CPU", "Commit1Yr", "h", 21900000, americas),
		catalogSKU("c2-v1-ram", "Commitment v1: Compute optimized Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 2800000, americas),
	}
	_, err := findFamilyRates(skus, "c2-standard-8", "us-central1", 1)
	require.ErrorContains(t, err, "ambiguous catalog")
}
