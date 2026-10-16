package services_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cache"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/compute"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/cosmosdb"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/database"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations/reservationstest"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/managedredis"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/search"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/synapse"
)

type purchaser interface {
	PurchaseCommitment(context.Context, common.Recommendation, common.PurchaseOptions) (common.PurchaseResult, error)
}

// Golden bodies were captured from the string-literal implementation before it
// switched to the SDK constants; the wire format must not change.
const purchaseBodyGolden = `{"location":"eastus","properties":{"appliedScopeType":"Shared","billingPlan":"Monthly","billingScopeId":"/subscriptions/sub","quantity":2,"renew":false,"reservedResourceType":"%s","term":"%s"},"sku":{"name":"GP_Gen5_8"},"tags":{"purchase-automation":"cudly-cli"}}`

func TestPurchaseBodies(t *testing.T) {
	cred := &fake.TokenCredential{}
	services := []struct {
		name, resourceType string
		build              func(h *reservationstest.Capture) purchaser
	}{
		{"cache", "RedisCache", func(h *reservationstest.Capture) purchaser { return cache.NewClientWithHTTP(cred, "sub", "eastus", h) }},
		{"compute", "VirtualMachines", func(h *reservationstest.Capture) purchaser {
			return compute.NewClientWithHTTP(cred, "sub", "eastus", h)
		}},
		{"cosmosdb", "CosmosDb", func(h *reservationstest.Capture) purchaser {
			return cosmosdb.NewClientWithHTTP(cred, "sub", "eastus", h)
		}},
		{"database", "SqlDatabases", func(h *reservationstest.Capture) purchaser {
			return database.NewClientWithHTTP(cred, "sub", "eastus", h)
		}},
		{"managedredis", "RedisCache", func(h *reservationstest.Capture) purchaser {
			return managedredis.NewClientWithHTTP(cred, "sub", "eastus", h)
		}},
		{"search", "SearchService", func(h *reservationstest.Capture) purchaser { return search.NewClientWithHTTP(cred, "sub", "eastus", h) }},
		{"synapse", "SqlDataWarehouse", func(h *reservationstest.Capture) purchaser {
			return synapse.NewClientWithHTTP(cred, "sub", "eastus", h)
		}},
	}
	purchase := func(s purchaser, term string) (common.PurchaseResult, error) {
		return s.PurchaseCommitment(context.Background(),
			common.Recommendation{ResourceType: "GP_Gen5_8", Term: term, Count: 2, PaymentOption: "no-upfront"},
			common.PurchaseOptions{Source: common.PurchaseSourceCLI})
	}
	for _, svc := range services {
		for term, want := range map[string]string{"1yr": "P1Y", "3yr": "P3Y"} {
			t.Run(svc.name+"/"+term, func(t *testing.T) {
				fakeARM := &reservationstest.Capture{}
				res, err := purchase(svc.build(fakeARM), term)
				require.NoError(t, err)
				require.True(t, res.Success)
				got, err := fakeARM.BodyWithoutDisplayName()
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf(purchaseBodyGolden, svc.resourceType, want), string(got))
			})
		}
		// Pins the early rejection by ParseTermYears before any request.
		t.Run(svc.name+"/unsupported-term", func(t *testing.T) {
			fakeARM := &reservationstest.Capture{}
			res, err := purchase(svc.build(fakeARM), "5yr")
			require.Error(t, err)
			require.False(t, res.Success)
			require.Zero(t, fakeARM.Calls)
		})
	}
}
