package computeengine

import (
	"context"
	"strings"
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
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient("p", "us-central1")
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
	t.Parallel()
	t.Run("region without SKUs", func(t *testing.T) {
		t.Parallel()
		_, err := findFamilyRates(n2AmericasSKUs(), "n2-standard-8", "asia-east1", 1)
		require.ErrorContains(t, err, "no on-demand core and RAM pricing")
	})
	t.Run("family without commitment SKUs", func(t *testing.T) {
		t.Parallel()
		_, err := findFamilyRates(n2AmericasSKUs()[:2], "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "no commitment pricing")
	})
	t.Run("unit mismatch", func(t *testing.T) {
		t.Parallel()
		skus := n2AmericasSKUs()
		skus[0].PricingInfo[0].PricingExpression.UsageUnit = "mo"
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "expected \"h\"")
	})
	t.Run("conflicting duplicate", func(t *testing.T) {
		t.Parallel()
		skus := append(n2AmericasSKUs(), catalogSKU("dup", "N2 Instance Core running in Americas", "CPU", "OnDemand", "h", 1, americas))
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "ambiguous catalog")
	})
	t.Run("usage type disagrees with description", func(t *testing.T) {
		t.Parallel()
		skus := n2AmericasSKUs()
		skus[2].Category.UsageType = "Commit3Yr"
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "usage type")
	})
	t.Run("missing currency", func(t *testing.T) {
		t.Parallel()
		skus := n2AmericasSKUs()
		skus[0].PricingInfo[0].PricingExpression.TieredRates[0].UnitPrice.CurrencyCode = ""
		_, err := findFamilyRates(skus, "n2-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "no currency")
	})
}

func TestResourcesFromRecommendation_RequiresCountAndMemory(t *testing.T) {
	t.Parallel()
	_, err := resourcesFromRecommendation(newRec("n2-standard-8", 0, 32))
	require.Error(t, err)
	_, err = resourcesFromRecommendation(newRec("n2-standard-8", 8, 0))
	require.Error(t, err)
}

// Monthly figures for n2-standard-8 (8 vCPU, 32 GiB), live-shaped rates, per term.
// A division by 12 instead of termYears*12 would triple the 3-year amounts.
func TestEnrichRecWithPricing_MonthlyFiguresPerTerm(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		term                   string
		commitHourly, odHourly float64
	}{
		"1yr": {"1yr", 8*0.019915 + 32*0.002669, 8*0.031611 + 32*0.004237},
		"3yr": {"3yr", 8*0.014225 + 32*0.001907, 8*0.031611 + 32*0.004237},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, _ := NewClient("p", "us-central1")
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

func TestEnrichRecWithPricing_TermSpellingsAndUnknown(t *testing.T) {
	t.Parallel()
	price := func(term string) *float64 {
		client, _ := NewClient("p", "us-central1")
		client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: n2AmericasSKUs()}})
		rec := newRec("n2-standard-8", 8, 32)
		rec.Term = term
		client.enrichRecWithPricing(context.Background(), &rec)
		return rec.RecurringMonthlyCost
	}
	one, three := price("1yr"), price("3yr")
	require.NotNil(t, one)
	require.NotNil(t, three)
	for _, term := range []string{"12mo", "12MO", " 1 "} {
		got := price(term)
		require.NotNil(t, got, term)
		assert.InDelta(t, *one, *got, 1e-9, term)
	}
	for _, term := range []string{"36mo", "36MO", " 3 "} {
		got := price(term)
		require.NotNil(t, got, term)
		assert.InDelta(t, *three, *got, 1e-9, term)
	}
	assert.Nil(t, price("2yr"), "unknown term must not be priced as 1yr")
	assert.Nil(t, price("24mo"), "unknown term must not be priced as 1yr")
}

func TestEnrichRecWithPricing_InvalidCurrency(t *testing.T) {
	t.Parallel()
	for _, currency := range []string{"", "EUR"} {
		t.Run("commitment currency "+currency, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			client, err := NewClient("test-project", "us-central1")
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
	t.Parallel()
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
			t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// c4dAmericasSKUs: live C4D SKUs for us-central1 (Americas, read 2026-11-08).
// C4D quotes RAM in decimal GBy.h, on demand and for commitments.
func c4dAmericasSKUs() []*cloudbilling.Sku {
	return []*cloudbilling.Sku{
		catalogSKU("80C2-B3C5-BD5E", "C4D Instance Core running in Americas", "CPU", "OnDemand", "h", 32703500, americas),
		catalogSKU("159A-6F40-D544", "C4D Instance Ram running in Americas", "RAM", "OnDemand", "GBy.h", 3495578, americas),
		catalogSKU("9865-5346-2DAF", "Commitment v1: C4D Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 20603200, americas),
		catalogSKU("1180-00E6-9F2F", "Commitment v1: C4D Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GBy.h", 2202232, americas),
		catalogSKU("353D-9F68-E3A8", "Commitment v1: C4D Cpu in Americas for 3 Years", "CPU", "Commit3Yr", "h", 14716600, americas),
		catalogSKU("D00F-CFA2-8CD4", "Commitment v1: C4D Ram in Americas for 3 Years", "RAM", "Commit3Yr", "GBy.h", 1572996, americas),
	}
}

// gibPerGB converts a per-GB (1e9 bytes) price to per-GiB (2^30 bytes).
const gibPerGB = (1 << 30) / 1e9

func TestFindFamilyRates_RAMUsageUnits(t *testing.T) {
	t.Parallel()

	t.Run("C4D GBy.h on demand and commitment convert to per GiB", func(t *testing.T) {
		t.Parallel()
		one, err := findFamilyRates(c4dAmericasSKUs(), "c4d-standard-8", "us-central1", 1)
		require.NoError(t, err)
		assert.InDelta(t, 0.0327035, one.onDemand.cpu, 1e-12)
		assert.InDelta(t, 0.003495578*gibPerGB, one.onDemand.ram, 1e-12)
		assert.InDelta(t, 0.0206032, one.commitment.cpu, 1e-12)
		assert.InDelta(t, 0.002202232*gibPerGB, one.commitment.ram, 1e-12)
		assert.InDelta(t, 0.0037533, one.onDemand.ram, 1e-6)

		three, err := findFamilyRates(c4dAmericasSKUs(), "c4d-standard-8", "us-central1", 3)
		require.NoError(t, err)
		assert.InDelta(t, 0.0147166, three.commitment.cpu, 1e-12)
		assert.InDelta(t, 0.001572996*gibPerGB, three.commitment.ram, 1e-12)
	})

	t.Run("N2 GiBy.h is unchanged", func(t *testing.T) {
		t.Parallel()
		r, err := findFamilyRates(n2AmericasSKUs(), "n2-standard-8", "us-central1", 1)
		require.NoError(t, err)
		assert.InDelta(t, 0.004237, r.onDemand.ram, 1e-12)
		assert.InDelta(t, 0.002669, r.commitment.ram, 1e-12)
	})

	t.Run("M4Ultramem224 GBy.h through the collector", func(t *testing.T) {
		t.Parallel()
		var one, three rateCollector
		require.NoError(t, one.add(catalogSKU("451F-FFFB-68C2", "Commitment v1: M4Ultramem224 Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GBy.h", 3348770, americas), true, true))
		require.NoError(t, three.add(catalogSKU("A188-9304-A5E6", "Commitment v1: M4Ultramem224 Ram in Americas for 3 Years", "RAM", "Commit3Yr", "GBy.h", 1702760, americas), true, true))
		assert.InDelta(t, 0.00334877*gibPerGB, one.cRAM.value, 1e-12)
		assert.InDelta(t, 0.00170276*gibPerGB, three.cRAM.value, 1e-12)
	})

	t.Run("helper converts per allowed unit", func(t *testing.T) {
		t.Parallel()
		got, err := ramPricePerGiBHour(0.00334877, "GBy.h", "451F-FFFB-68C2")
		require.NoError(t, err)
		assert.InDelta(t, 0.00334877*1.073741824, got, 1e-15)
		got, err = ramPricePerGiBHour(0.004237, "GiBy.h", "x")
		require.NoError(t, err)
		assert.Equal(t, 0.004237, got)
	})

	for _, unit := range []string{"MiBy.h", "GiBy.mo", ""} {
		t.Run("unsupported RAM unit "+unit, func(t *testing.T) {
			t.Parallel()
			skus := c4dAmericasSKUs()
			skus[1].PricingInfo[0].PricingExpression.UsageUnit = unit
			_, err := findFamilyRates(skus, "c4d-standard-8", "us-central1", 1)
			require.ErrorContains(t, err, "unsupported RAM usage unit")
		})
	}

	t.Run("mixed units at the same raw price are ambiguous", func(t *testing.T) {
		t.Parallel()
		skus := append(c4dAmericasSKUs(),
			catalogSKU("dup-gib", "C4D Instance Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 3495578, americas))
		_, err := findFamilyRates(skus, "c4d-standard-8", "us-central1", 1)
		require.ErrorContains(t, err, "ambiguous catalog")
	})
}

// Live us-central1 C4D rows, c4d-standard-8 (8 vCPU, 30 GiB), 1 year:
//
//	on demand  8*0.0327035 + 30*0.003495578*1.073741824
//	commitment 8*0.0206032 + 30*0.002202232*1.073741824
func TestGetComputePricing_C4DGBytePricedRAM(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	client, _ := NewClient("p", "us-central1")
	client.SetBillingService(&MockBillingService{skus: &cloudbilling.ListSkusResponse{Skus: c4dAmericasSKUs()}})
	res := computeResources{vcpus: 8, memoryGiB: 30}

	one, err := client.getComputePricing(ctx, "c4d-standard-8", "us-central1", 1, res)
	require.NoError(t, err)
	wantCommit := 8*0.0206032 + 30*0.002202232*gibPerGB
	wantOD := 8*0.0327035 + 30*0.003495578*gibPerGB
	assert.Equal(t, "USD", one.Currency)
	assert.InDelta(t, wantCommit, one.HourlyRate, 1e-9)
	assert.InDelta(t, wantCommit*8760, one.CommitmentPrice, 1e-6)
	assert.InDelta(t, wantOD*8760, one.OnDemandPrice, 1e-6)
	assert.Greater(t, one.SavingsPercentage, 0.0)

	three, err := client.getComputePricing(ctx, "c4d-standard-8", "us-central1", 3, res)
	require.NoError(t, err)
	assert.InDelta(t, (8*0.0147166+30*0.001572996*gibPerGB)*8760*3, three.CommitmentPrice, 1e-6)
	assert.Greater(t, three.SavingsPercentage, one.SavingsPercentage)
}

// m4AndUltramemSKUs carries plain M4 rows next to M4Ultramem224 rows. The
// catalog writes the Ultramem family with no space after "M4". RAM is GiBy.h
// here; TestFindFamilyRates_M4UltramemGBHRamPrices covers the live GBy.h unit.
func m4AndUltramemSKUs() []*cloudbilling.Sku {
	return []*cloudbilling.Sku{
		catalogSKU("m4-od-core", "M4 Instance Core running in Americas", "CPU", "OnDemand", "h", 4000000, americas),
		catalogSKU("m4-od-ram", "M4 Instance Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 4570000, americas),
		catalogSKU("m4-c1-core", "Commitment v1: M4 Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 2500000, americas),
		catalogSKU("m4-c1-ram", "Commitment v1: M4 Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 2900000, americas),
		catalogSKU("mu-od-core", "M4Ultramem224 Instance Core running in Americas", "CPU", "OnDemand", "h", 8000000, americas),
		catalogSKU("mu-od-ram", "M4Ultramem224 Instance Ram running in Americas", "RAM", "OnDemand", "GiBy.h", 5675880, americas),
		catalogSKU("mu-c1-core", "Commitment v1: M4Ultramem224 Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 5000000, americas),
		catalogSKU("mu-c1-ram", "Commitment v1: M4Ultramem224 Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 3500000, americas),
	}
}

func TestFamilyLabels_M4Ultramem(t *testing.T) {
	t.Parallel()
	labels, err := familyLabels("m4-ultramem-224")
	require.NoError(t, err)
	assert.Equal(t, "M4Ultramem224", labels.onDemand)
	assert.Equal(t, []string{"M4Ultramem224"}, labels.commit)

	for _, mt := range []string{"m4-ultramem-999", "m4-ultramem", "m4-ultramem-", "m4n-ultramem-224"} {
		_, err := familyLabels(mt)
		require.ErrorIs(t, err, errUnknownCatalogFamily, mt)
	}
}

func TestClassify_M4UltramemNeverMatchesPlainM4AndViceVersa(t *testing.T) {
	t.Parallel()
	ultra, err := familyLabels("m4-ultramem-224")
	require.NoError(t, err)
	plain, err := familyLabels("m4-megamem-28")
	require.NoError(t, err)

	for _, sku := range m4AndUltramemSKUs() {
		isUltraSKU := strings.Contains(sku.Description, "M4Ultramem224")
		_, _, gotUltra, err := newRateMatcher(ultra, "us-central1", 1).classify(sku)
		require.NoError(t, err)
		assert.Equal(t, isUltraSKU, gotUltra, "ultramem shape vs %q", sku.Description)
		_, _, gotPlain, err := newRateMatcher(plain, "us-central1", 1).classify(sku)
		require.NoError(t, err)
		assert.Equal(t, !isUltraSKU, gotPlain, "plain m4 shape vs %q", sku.Description)
	}
}

func TestFindFamilyRates_M4UltramemUsesOwnRows(t *testing.T) {
	t.Parallel()
	skus := m4AndUltramemSKUs()
	ultra, err := findFamilyRates(skus, "m4-ultramem-224", "us-central1", 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.008, ultra.onDemand.cpu, 1e-9)
	assert.InDelta(t, 0.005, ultra.commitment.cpu, 1e-9)

	plain, err := findFamilyRates(skus, "m4-megamem-28", "us-central1", 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.004, plain.onDemand.cpu, 1e-9)
	assert.InDelta(t, 0.0025, plain.commitment.cpu, 1e-9)

	_, err = findFamilyRates(skus, "m4-ultramem-999", "us-central1", 1)
	require.ErrorIs(t, err, errUnknownCatalogFamily)
}

func TestFamilyLabels_NonM4UltramemKeepOverrides(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"m1-ultramem-40":  "Memory-optimized",
		"m2-ultramem-208": "Memory-optimized",
		"m3-ultramem-32":  "M3 Memory-optimized",
	}
	for mt, want := range cases {
		labels, err := familyLabels(mt)
		require.NoError(t, err, mt)
		assert.Equal(t, want, labels.onDemand, mt)
		assert.Equal(t, []string{want}, labels.commit, mt)
	}
}

func TestFindFamilyRates_M4UltramemGBHRamPrices(t *testing.T) {
	t.Parallel()
	skus := m4AndUltramemSKUs()
	for _, sku := range skus {
		if sku.SkuId == "mu-od-ram" || sku.SkuId == "mu-c1-ram" {
			sku.PricingInfo[0].PricingExpression.UsageUnit = "GBy.h"
		}
	}
	got, err := findFamilyRates(skus, "m4-ultramem-224", "us-central1", 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.008, got.onDemand.cpu, 1e-9)
	assert.InDelta(t, 0.005, got.commitment.cpu, 1e-9)
	assert.InDelta(t, 0.00567588*gibPerGB, got.onDemand.ram, 1e-12)
	assert.InDelta(t, 0.0035*gibPerGB, got.commitment.ram, 1e-12)
}
