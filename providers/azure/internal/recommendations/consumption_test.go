package recommendations

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/mocks"
)

// Window amounts from the dev screenshot (Standard_D2as_v4, eastus, 12
// instances, 7-day window): $191 on demand, $116 with the 1y reservation.
const (
	rawOnDemand = 191.0
	rawTotal    = 116.0
)

func i32(v int32) *int32 { return &v }

// consumptionRecs returns the same recommendation in both response shapes.
func consumptionRecs(days int, term string) map[string]armconsumption.ReservationRecommendationClassification {
	legacy := map[int]string{7: "Last7Days", 30: "Last30Days", 60: "Last60Days"}[days]
	return map[string]armconsumption.ReservationRecommendationClassification{
		"legacy": mocks.BuildLegacyReservationRecommendation(
			mocks.WithTerm(term), mocks.WithQuantity(12), mocks.WithSKU("Standard_D2as_v4"),
			mocks.WithCosts(rawOnDemand, rawTotal, rawOnDemand-rawTotal), mocks.WithLookBack(legacy)),
		"modern": mocks.BuildModernReservationRecommendation(
			mocks.WithModernTerm(term), mocks.WithModernQuantity(12), mocks.WithModernSKUName("Standard_D2as_v4"),
			mocks.WithModernCosts(rawOnDemand, rawTotal, rawOnDemand-rawTotal), mocks.WithModernLookBack(i32(int32(days)))),
	}
}

func TestExtractConsumption_ScalesWindowAmountsToMonthly(t *testing.T) {
	for _, days := range []int{7, 30, 60} {
		for shape, rec := range consumptionRecs(days, "P1Y") {
			t.Run(fmt.Sprintf("%s/L%d", shape, days), func(t *testing.T) {
				f, err := ExtractConsumption(rec)
				require.NoError(t, err)
				require.NotNil(t, f)
				scale := DaysPerMonth / float64(days)
				assert.InDelta(t, rawOnDemand*scale, f.OnDemandCost, 1e-9)
				assert.InDelta(t, (rawOnDemand-rawTotal)*scale, f.EstimatedSavings, 1e-9)
				assert.Zero(t, f.CommitmentCost, "commitment comes from the retail price, not the window total")
				assert.Nil(t, f.RecurringMonthlyCost)
				assert.Equal(t, days, f.LookBackDays)
				assert.Equal(t, "1yr", f.Term)
			})
		}
	}
}

func TestExtractConsumption_SkipsWithError(t *testing.T) {
	usd := mocks.WithModernCosts(rawOnDemand, rawTotal, 75)
	cases := map[string]armconsumption.ReservationRecommendationClassification{
		"legacy missing lookback": mocks.BuildLegacyReservationRecommendation(mocks.WithCosts(191, 116, 75), mocks.WithLookBack("")),
		"legacy unknown lookback": mocks.BuildLegacyReservationRecommendation(mocks.WithCosts(191, 116, 75), mocks.WithLookBack("Last90Days")),
		"legacy unknown term":     mocks.BuildLegacyReservationRecommendation(mocks.WithCosts(191, 116, 75), mocks.WithTerm("P2Y")),
		"legacy missing costs":    mocks.BuildLegacyReservationRecommendation(),
		"modern missing lookback": mocks.BuildModernReservationRecommendation(usd, mocks.WithModernLookBack(nil)),
		"modern unknown lookback": mocks.BuildModernReservationRecommendation(usd, mocks.WithModernLookBack(i32(14))),
		"modern unknown term":     mocks.BuildModernReservationRecommendation(usd, mocks.WithModernTerm("P5Y")),
		"modern non-USD":          mocks.BuildModernReservationRecommendation(usd, mocks.WithModernCurrency("EUR")),
		"modern missing currency": mocks.BuildModernReservationRecommendation(usd, mocks.WithModernCurrency("")),
		"modern missing costs":    mocks.BuildModernReservationRecommendation(),
	}
	for name, rec := range cases {
		t.Run(name, func(t *testing.T) {
			f, err := ExtractConsumption(rec)
			assert.Error(t, err)
			assert.Nil(t, f)
		})
	}
}

func TestExtractConsumption_RefusedPayloadsAreNilWithoutError(t *testing.T) {
	f, err := ExtractConsumption(nil)
	assert.NoError(t, err)
	assert.Nil(t, f)
}

// stubPrices serves 1y/3y unit prices from the retail rows of the screenshot's
// SKU and counts lookups per key.
type stubPrices struct {
	calls map[string]int
	fail  map[string]bool
	cur   string
}

func (s *stubPrices) lookup(_ context.Context, sku, region string, years int) (ReservationPrice, error) {
	key := fmt.Sprintf("%s/%s/%dy", sku, region, years)
	s.calls[key]++
	if s.fail[sku] {
		return ReservationPrice{}, errors.New("no reservation pricing found")
	}
	cur := s.cur
	if cur == "" {
		cur = "USD"
	}
	return ReservationPrice{Total: map[int]float64{1: 494, 3: 949}[years], Currency: cur}, nil
}

func newStub() *stubPrices { return &stubPrices{calls: map[string]int{}, fail: map[string]bool{}} }

func extractedBase(t *testing.T, rec armconsumption.ReservationRecommendationClassification) common.Recommendation {
	t.Helper()
	f, err := ExtractConsumption(rec)
	require.NoError(t, err)
	return common.Recommendation{
		Region: f.Region, ResourceType: f.ResourceType, Count: f.Count, Term: f.Term,
		OnDemandCost: f.OnDemandCost, EstimatedSavings: f.EstimatedSavings,
	}
}

func byPayment(recs []common.Recommendation) map[string]common.Recommendation {
	out := map[string]common.Recommendation{}
	for _, r := range recs {
		out[r.PaymentOption] = r
	}
	return out
}

// The screenshot case: 12 x Standard_D2as_v4, 7-day window, $191 on demand,
// reservation $494 (1y) / $949 (3y) per unit.
func TestExpandConsumptionVariants_ScreenshotWorkedExample(t *testing.T) {
	for shape, rec := range consumptionRecs(7, "P1Y") {
		t.Run(shape+"/1y", func(t *testing.T) {
			stub := newStub()
			variants, err := ExpandConsumptionVariants(context.Background(), extractedBase(t, rec), NewPricer("compute", stub.lookup))
			require.NoError(t, err)
			v := byPayment(variants)
			require.Len(t, variants, 2)

			assert.InDelta(t, 191*30.4375/7, v["upfront"].OnDemandCost, 1e-9)
			assert.InDelta(t, 75*30.4375/7, v["upfront"].EstimatedSavings, 1e-9)
			assert.InDelta(t, 12*494, v["upfront"].CommitmentCost, 1e-9)
			require.NotNil(t, v["upfront"].RecurringMonthlyCost)
			assert.Zero(t, *v["upfront"].RecurringMonthlyCost)

			assert.Zero(t, v["monthly"].CommitmentCost)
			require.NotNil(t, v["monthly"].RecurringMonthlyCost)
			assert.InDelta(t, 494.0, *v["monthly"].RecurringMonthlyCost, 1e-9)
			assert.InDelta(t, v["upfront"].OnDemandCost, v["monthly"].OnDemandCost, 1e-9)
			assert.InDelta(t, v["upfront"].EstimatedSavings, v["monthly"].EstimatedSavings, 1e-9)
		})
	}
}

func TestExpandConsumptionVariants_ThreeYearUpfrontExceedsOneYear(t *testing.T) {
	for _, days := range []int{7, 30, 60} {
		for shape, rec3 := range consumptionRecs(days, "P3Y") {
			t.Run(fmt.Sprintf("%s/L%d", shape, days), func(t *testing.T) {
				stub := newStub()
				p := NewPricer("compute", stub.lookup)
				three, err := ExpandConsumptionVariants(context.Background(), extractedBase(t, rec3), p)
				require.NoError(t, err)
				one, err := ExpandConsumptionVariants(context.Background(), extractedBase(t, consumptionRecs(days, "P1Y")[shape]), p)
				require.NoError(t, err)

				v3, v1 := byPayment(three), byPayment(one)
				assert.InDelta(t, 12*949, v3["upfront"].CommitmentCost, 1e-9)
				assert.Greater(t, v3["upfront"].CommitmentCost, v1["upfront"].CommitmentCost)
				assert.Zero(t, v3["monthly"].CommitmentCost)
				assert.InDelta(t, 12*949.0/36, *v3["monthly"].RecurringMonthlyCost, 1e-9)
				assert.Greater(t, *v3["monthly"].RecurringMonthlyCost, 0.0)
				assert.Zero(t, *v3["upfront"].RecurringMonthlyCost)
			})
		}
	}
}

func TestPricer_DeduplicatesLookupsPerKey(t *testing.T) {
	stub := newStub()
	p := NewPricer("compute", stub.lookup)
	base := extractedBase(t, consumptionRecs(7, "P1Y")["legacy"])
	for range 3 {
		_, err := ExpandConsumptionVariants(context.Background(), base, p)
		require.NoError(t, err)
	}
	assert.Equal(t, map[string]int{"Standard_D2as_v4/eastus/1y": 1}, stub.calls)

	stub.fail["Standard_Bad"] = true
	bad := base
	bad.ResourceType = "Standard_Bad"
	for range 2 {
		_, err := ExpandConsumptionVariants(context.Background(), bad, p)
		require.Error(t, err)
	}
	assert.Equal(t, 1, stub.calls["Standard_Bad/eastus/1y"], "failures are cached too")
}

func TestExpandConsumptionVariants_Errors(t *testing.T) {
	base := extractedBase(t, consumptionRecs(7, "P1Y")["legacy"])
	t.Run("lookup failure", func(t *testing.T) {
		stub := newStub()
		stub.fail[base.ResourceType] = true
		_, err := ExpandConsumptionVariants(context.Background(), base, NewPricer("compute", stub.lookup))
		assert.ErrorContains(t, err, "no reservation pricing found")
	})
	t.Run("non-USD price row", func(t *testing.T) {
		stub := newStub()
		stub.cur = "EUR"
		_, err := ExpandConsumptionVariants(context.Background(), base, NewPricer("compute", stub.lookup))
		assert.ErrorContains(t, err, "not USD")
	})
	t.Run("zero count", func(t *testing.T) {
		b := base
		b.Count = 0
		_, err := ExpandConsumptionVariants(context.Background(), b, NewPricer("compute", newStub().lookup))
		assert.ErrorContains(t, err, "non-positive count")
	})
	t.Run("unknown term", func(t *testing.T) {
		b := base
		b.Term = "2yr"
		_, err := ExpandConsumptionVariants(context.Background(), b, NewPricer("compute", newStub().lookup))
		assert.ErrorContains(t, err, "unrecognized term")
	})
}

func TestAppendConsumptionVariants_SkipsOnlyTheUnpriceable(t *testing.T) {
	stub := newStub()
	stub.fail["Standard_Bad"] = true
	good := extractedBase(t, consumptionRecs(7, "P1Y")["legacy"])
	bad := good
	bad.ResourceType = "Standard_Bad"

	p := NewPricer("compute", stub.lookup)
	var recs []common.Recommendation
	recs, err := AppendConsumptionVariants(context.Background(), "compute", recs, bad, p)
	require.NoError(t, err, "an unresolvable price skips only that recommendation")
	recs, err = AppendConsumptionVariants(context.Background(), "compute", recs, good, p)
	require.NoError(t, err)
	require.Len(t, recs, 2)
	assert.Equal(t, "Standard_D2as_v4", recs[0].ResourceType)
}

func TestConsumptionFilter_SetsLookBackExplicitly(t *testing.T) {
	assert.Equal(t,
		"properties/scope eq 'Shared' and properties/resourceType eq 'VirtualMachines' and properties/lookBackPeriod eq 'Last7Days'",
		ConsumptionFilter("VirtualMachines"))
}

func TestPricer_ZeroPriceIsUnresolvable(t *testing.T) {
	base := extractedBase(t, consumptionRecs(7, "P1Y")["legacy"])
	p := NewPricer("compute", func(context.Context, string, string, int) (ReservationPrice, error) {
		return ReservationPrice{Total: 0, Currency: "USD"}, nil
	})
	var recs []common.Recommendation
	recs, err := AppendConsumptionVariants(context.Background(), "compute", recs, base, p)
	require.NoError(t, err)
	assert.Empty(t, recs, "a zero price must skip the recommendation, not yield a free commitment")

	_, err = ExpandConsumptionVariants(context.Background(), base, p)
	assert.ErrorContains(t, err, "not positive")
}

// A lookup that fails to complete must fail the collection and must not be
// cached as if the SKU had no price.
func TestAppendConsumptionVariants_LookupFailureFailsCollection(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := map[string]error{
		"fetch failure":     fmt.Errorf("%w: pricing API returned status 500", pricing.ErrFetch),
		"context canceled":  context.Canceled,
		"deadline exceeded": context.DeadlineExceeded,
		"canceled context":  canceled.Err(),
	}
	for name, lookupErr := range cases {
		t.Run(name, func(t *testing.T) {
			calls := 0
			p := NewPricer("compute", func(context.Context, string, string, int) (ReservationPrice, error) {
				calls++
				return ReservationPrice{}, lookupErr
			})
			base := extractedBase(t, consumptionRecs(7, "P1Y")["legacy"])
			for range 2 {
				recs, err := AppendConsumptionVariants(context.Background(), "compute", nil, base, p)
				require.Error(t, err)
				assert.True(t, IsCollectionFailure(err))
				assert.Empty(t, recs)
			}
			assert.Equal(t, 2, calls, "a failed fetch is not cached")
		})
	}
}
