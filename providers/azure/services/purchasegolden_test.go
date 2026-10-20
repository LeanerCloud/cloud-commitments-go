package services_test

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

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

const (
	goldenToken = "golden-token-1"
	// goldenMarker is "cudly" + the first 16 hex digits of SHA-256(goldenToken).
	goldenMarker = "cudlycedb15622ab2dfb6"

	armBase        = "https://management.azure.com"
	urlProvider    = armBase + "/subscriptions/sub/providers/Microsoft.Capacity?api-version=2021-04-01"
	urlOrders      = armBase + "/providers/Microsoft.Capacity/reservationOrders?api-version=2022-11-01"
	urlCalculate   = armBase + "/providers/Microsoft.Capacity/calculatePrice?api-version=2022-11-01"
	urlPurchase    = armBase + "/providers/Microsoft.Capacity/reservationOrders/captured-order/purchase?api-version=2022-11-01"
	methodGet      = "GET"
	methodPost     = "POST"
	tokenBodyTmpl  = `{"location":"eastus","properties":{"appliedScopeType":"Shared","billingPlan":"Monthly","billingScopeId":"/subscriptions/sub","displayName":"%s","quantity":2,"renew":false,"reservedResourceType":"%s","term":"P1Y"},"sku":{"name":"GP_Gen5_8"},"tags":{"cudly-idempotency-token":"` + goldenToken + `","purchase-automation":"cudly-cli"}}`
	noNameBodyTmpl = `{"location":"eastus","properties":{"appliedScopeType":"Shared","billingPlan":"Monthly","billingScopeId":"/subscriptions/sub","quantity":2,"renew":false,"reservedResourceType":"%s","term":"P1Y"},"sku":{"name":"GP_Gen5_8"},"tags":{"purchase-automation":"cudly-cli"}}`
)

type goldenCase struct {
	name         string
	resourceType string
	build        func(cred *reservationstest.CountingCred, h *reservationstest.Capture) purchaser
	// tokenName is the exact displayName when IdempotencyToken is set.
	tokenName string
	// plainRE matches the displayName with no token, which embeds the wall
	// clock and a random suffix.
	plainRE *regexp.Regexp
	// preflight lists requests that precede the idempotency flow (compute's
	// provider registration check).
	preflight []string
}

func goldenCases() []goldenCase {
	return []goldenCase{
		{"cache", "RedisCache", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return cache.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "redis-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^redis-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
		{"compute", "VirtualMachines", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return compute.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "vm-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^vm-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), []string{methodGet + " " + urlProvider}},
		{"cosmosdb", "CosmosDb", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return cosmosdb.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "cosmos-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^cosmos-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
		{"database", "SqlDatabases", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return database.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "sql-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^sql-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
		{"managedredis", "RedisCache", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return managedredis.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "redis-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^redis-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
		{"search", "SearchService", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return search.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "search-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^search-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
		{"synapse", "SqlDataWarehouse", func(c *reservationstest.CountingCred, h *reservationstest.Capture) purchaser {
			return synapse.NewClientWithHTTP(c, "sub", "eastus", h)
		}, "synapse-eastus-GP_Gen5_8-2x-1yr-monthly-" + goldenMarker, regexp.MustCompile(`^synapse-eastus-GP_Gen5_8-2x-1yr-monthly-\d{8}T\d{6}-[0-9a-f]{8}$`), nil},
	}
}

func goldenPurchase(t *testing.T, gc goldenCase, token string) (*reservationstest.Capture, *reservationstest.CountingCred) {
	t.Helper()
	h := &reservationstest.Capture{}
	cred := &reservationstest.CountingCred{}
	res, err := gc.build(cred, h).PurchaseCommitment(context.Background(),
		common.Recommendation{ResourceType: "GP_Gen5_8", Term: "1yr", Count: 2, PaymentOption: "monthly"},
		common.PurchaseOptions{Source: common.PurchaseSourceCLI, IdempotencyToken: token})
	require.NoError(t, err)
	require.True(t, res.Success)
	require.Equal(t, "captured-order", res.CommitmentID)
	return h, cred
}

// With a token the display name is the BuildDisplayName output with its
// random and timestamp segments dropped to make room for the token marker,
// so the whole body is deterministic.
func TestPurchaseGoldenWithToken(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		t.Run(gc.name, func(t *testing.T) {
			h, cred := goldenPurchase(t, gc, goldenToken)
			want := fmt.Sprintf(tokenBodyTmpl, gc.tokenName, gc.resourceType)
			require.Equal(t, want, string(h.BodyBytes))

			seq := append(append([]string{}, gc.preflight...),
				methodGet+" "+urlOrders, methodPost+" "+urlCalculate, methodPost+" "+urlPurchase)
			require.Equal(t, seq, h.Sequence())
			// calculatePrice and purchase carry the identical body.
			require.Equal(t, want, string(h.Requests[len(h.Requests)-1].Body))
			wantTokens := 1
			if gc.name == "compute" {
				wantTokens = 2
			}
			require.Equal(t, wantTokens, cred.Calls)
		})
	}
}

func TestPurchaseGoldenWithoutToken(t *testing.T) {
	t.Parallel()
	for _, gc := range goldenCases() {
		t.Run(gc.name, func(t *testing.T) {
			h, cred := goldenPurchase(t, gc, "")
			wantTokens := 1
			if gc.name == "compute" {
				wantTokens = 2
			}
			require.Equal(t, wantTokens, cred.Calls)
			// Pin the rest of the body exactly and the name by pattern.
			stripped, err := h.BodyWithoutDisplayName()
			require.NoError(t, err)
			require.Equal(t, fmt.Sprintf(noNameBodyTmpl, gc.resourceType), string(stripped))
			var body struct {
				Properties struct {
					DisplayName string `json:"displayName"`
				} `json:"properties"`
			}
			require.NoError(t, json.Unmarshal(h.BodyBytes, &body))
			require.Regexp(t, gc.plainRE, body.Properties.DisplayName)
			seq := append(append([]string{}, gc.preflight...), methodPost+" "+urlCalculate, methodPost+" "+urlPurchase)
			require.Equal(t, seq, h.Sequence())
			require.Equal(t, string(h.BodyBytes), string(h.Requests[len(h.Requests)-1].Body))
		})
	}
}
