package azure

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func riRow(service common.ServiceType, region, term, sku string, count int, payment string) common.Recommendation {
	return common.Recommendation{Provider: common.ProviderAzure, Service: service, CommitmentType: common.CommitmentReservedInstance,
		Account: "sub", Region: region, Term: term, ResourceType: sku, Count: count, PaymentOption: payment}
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

	t.Run("same-SKU duplicates collapse to the smallest count, keeping its payment variants and ties to the first", func(t *testing.T) {
		advisor := []common.Recommendation{
			riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 4, "upfront"), riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 4, "monthly"),
			riRow(vm, "eastus", "1yr", "standard_d2s_v3", 2, "upfront"), riRow(vm, "eastus", "1yr", "standard_d2s_v3", 2, "monthly"),
			riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 2, "upfront"), riRow(vm, "eastus", "1yr", "Standard_D2s_v3", 2, "monthly"),
			riRow(vm, "eastus", "1yr", "Standard_D8s_v3", 9, "upfront"),
		}
		got := dropCoveredAdvisorReservations(advisor, nil)
		assert.Equal(t, []common.Recommendation{advisor[2], advisor[3], advisor[6]}, got)
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
	assert.Equal(t, []common.Recommendation{advisor}, run(&fakeServiceClient{err: errors.New("boom")}, advisor),
		"a failed compute service leaves the Advisor row as the only source")
}
