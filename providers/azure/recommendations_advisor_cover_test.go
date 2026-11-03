package azure

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	azrecs "github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
)

func riRow(service common.ServiceType, region, term, sku string, count int, payment string) common.Recommendation {
	return common.Recommendation{Provider: common.ProviderAzure, Service: service, CommitmentType: common.CommitmentReservedInstance,
		Account: "sub", Region: region, Term: term, ResourceType: sku, Count: count, PaymentOption: payment}
}

// pricedFake is a service client that can also price Advisor rows at a flat USD 100 per unit.
type pricedFake struct{ fakeServiceClient }

func (*pricedFake) ReservationUnitPrice(context.Context, string, string, int) (azrecs.ReservationPrice, error) {
	return usd(100), nil
}

func TestResolveAdvisorRegion_NormalizesToARMForm(t *testing.T) {
	t.Parallel()
	got := resolveAdvisorRegion(advisorRec(subscriptionScope, reservationExt(map[string]*string{"region": strPtr("East US 2")})))
	assert.Equal(t, "eastus2", got)
}

func TestDropCoveredAdvisorReservations(t *testing.T) {
	t.Parallel()
	vm := common.ServiceCompute
	consumption := []common.Recommendation{riRow(vm, "eastus", "1yr", "Standard_D4s_v3", 1, "upfront")}

	t.Run("same key dropped even when the SKU differs; other region, term and service kept", func(t *testing.T) {
		keepRegion := riRow(vm, "westus", "1yr", "Standard_D2s_v3", 1, "")
		keepTerm := riRow(vm, "eastus", "3yr", "Standard_D2s_v3", 1, "")
		keepService := riRow(common.ServiceRelationalDB, "eastus", "1yr", "GP_Gen5_2", 1, "")
		advisor := []common.Recommendation{riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 1, ""), keepRegion, keepTerm, keepService}
		assert.Equal(t, []common.Recommendation{keepRegion, keepTerm, keepService}, dropCoveredAdvisorReservations(advisor, consumption))
	})

	t.Run("savings plan rows are untouched", func(t *testing.T) {
		sp := common.Recommendation{CommitmentType: common.CommitmentSavingsPlan, Service: vm, Account: "sub", Region: "eastus", Term: "1yr"}
		assert.Equal(t, []common.Recommendation{sp}, dropCoveredAdvisorReservations([]common.Recommendation{sp}, consumption))
	})
}

func TestGetRecommendations_AdvisorReservationCoveredByConsumption(t *testing.T) {
	vm := common.ServiceCompute
	run := func(compute serviceRecsGetter, advisor ...common.Recommendation) []common.Recommendation {
		overrideServiceClientFns(t, compute, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
		a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub",
			getAdvisorRecsFn: func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
				return advisor, nil
			}}
		recs, _ := a.GetRecommendations(context.Background(), &common.RecommendationParams{})
		return recs
	}
	consumption := riRow(vm, "eastus", "1yr", "Standard_D4s_v3", 1, "upfront")
	advisor := riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 2, "")

	assert.Equal(t, []common.Recommendation{consumption}, run(&fakeServiceClient{recs: []common.Recommendation{consumption}}, advisor))
	got := run(&pricedFake{fakeServiceClient{err: errors.New("boom")}}, advisor)
	require.Len(t, got, 2, "a failed compute service leaves the Advisor row as the only source")
	assert.Equal(t, "Standard_D2s_v3", got[0].ResourceType)
}

func TestGetRecommendations_FailedServiceRowsDoNotCoverAdvisor(t *testing.T) {
	consumption := riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D4s_v3", 1, "upfront")
	advisor := riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 2, "")
	overrideServiceClientFns(t, &pricedFake{fakeServiceClient{recs: []common.Recommendation{consumption}, err: errors.New("mid-walk failure")}},
		&fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	a := &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub",
		getAdvisorRecsFn: func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
			return []common.Recommendation{advisor}, nil
		}}

	recs, _ := a.GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.Len(t, recs, 2, "rows of a failed service are dropped, so they cover nothing")
	assert.Equal(t, "Standard_D2s_v3", recs[0].ResourceType)
}

type funcPriceFake struct {
	fakeServiceClient
	price azrecs.PriceFunc
}

func (f *funcPriceFake) ReservationUnitPrice(ctx context.Context, sku, region string, years int) (azrecs.ReservationPrice, error) {
	return f.price(ctx, sku, region, years)
}

func advisorOnlyAdapter(t *testing.T, compute serviceRecsGetter, advisor ...common.Recommendation) *RecommendationsClientAdapter {
	t.Helper()
	overrideServiceClientFns(t, compute, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{}, &fakeServiceClient{})
	return &RecommendationsClientAdapter{cred: &mockAzureTokenCredential{}, subscriptionID: "sub",
		getAdvisorRecsFn: func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
			return advisor, nil
		}}
}

func TestGetRecommendations_AdvisorPricingEndToEnd(t *testing.T) {
	priced := riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 3, "")
	unpriced := riRow(common.ServiceCompute, "westus", "1yr", "Standard_D2s_v3", 1, "")
	noUnit := &funcPriceFake{price: func(_ context.Context, _, region string, _ int) (azrecs.ReservationPrice, error) {
		if region == "westus" {
			return azrecs.ReservationPrice{}, errors.New("no reservation pricing found")
		}
		return usd(120), nil
	}}

	recs, err := advisorOnlyAdapter(t, noUnit, priced, unpriced).GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.Len(t, recs, 2, "the priced row's two legs; the unpriceable row is absent")
	assert.Equal(t, 360.0, recs[0].CommitmentCost)
	assert.Equal(t, "USD", recs[0].CommitmentCurrency)
	assert.Equal(t, "USD", recs[1].CommitmentCurrency)
	var partial *azrecs.PartialResultError
	require.True(t, errors.As(err, &partial))
	assert.Len(t, partial.Skipped, 1)
}

func TestGetRecommendations_CancelDuringAdvisorPricingReturnsCtxError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	canceling := &funcPriceFake{price: func(context.Context, string, string, int) (azrecs.ReservationPrice, error) {
		cancel()
		return usd(100), nil
	}}

	recs, err := advisorOnlyAdapter(t, canceling, riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 1, "")).
		GetRecommendations(ctx, &common.RecommendationParams{})

	assert.Nil(t, recs)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestGetRecommendations_FailedAdvisorCollectorRowsAreNotPriced(t *testing.T) {
	calls := 0
	counting := &funcPriceFake{price: func(context.Context, string, string, int) (azrecs.ReservationPrice, error) {
		calls++
		return usd(100), nil
	}}
	a := advisorOnlyAdapter(t, counting)
	a.getAdvisorRecsFn = func(context.Context, common.RecommendationParams) ([]common.Recommendation, error) {
		return []common.Recommendation{riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 1, "")}, errors.New("advisor list failed")
	}

	recs, err := a.GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err, "no skip is recorded, so no partial-result error")
	assert.Empty(t, recs)
	assert.Zero(t, calls)
}

func TestGetRecommendations_AdvisorPricingUsesParentContext(t *testing.T) {
	ctxAware := &funcPriceFake{price: func(ctx context.Context, _, _ string, _ int) (azrecs.ReservationPrice, error) {
		if err := ctx.Err(); err != nil {
			return azrecs.ReservationPrice{}, fmt.Errorf("lookup: %w", err)
		}
		return usd(100), nil
	}}

	recs, err := advisorOnlyAdapter(t, ctxAware, riRow(common.ServiceCompute, "eastus", "1yr", "Standard_D2s_v3", 2, "")).
		GetRecommendations(context.Background(), &common.RecommendationParams{})

	require.NoError(t, err, "no skip recorded: the errgroup context is canceled once Wait returns, so pricing must use the parent")
	require.Len(t, recs, 2)
	assert.Equal(t, "USD", recs[0].CommitmentCurrency)
	assert.Positive(t, recs[0].CommitmentCost)
}
