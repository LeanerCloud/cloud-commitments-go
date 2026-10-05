package opensearch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/opensearch"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/opensearch/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const offeringPath = "/2021-01-01/opensearch/reservedInstanceOfferings"
const purchasePath = "/2021-01-01/opensearch/purchaseReservedInstanceOffering"
const selectedDuration = 31536000

type fixtureOffering struct {
	ID           string  `json:"ReservedInstanceOfferingId"`
	InstanceType string  `json:"InstanceType"`
	Payment      string  `json:"PaymentOption"`
	Duration     int32   `json:"Duration"`
	FixedPrice   float64 `json:"FixedPrice"`
	UsagePrice   float64 `json:"UsagePrice"`
	Currency     string  `json:"CurrencyCode"`
}

type fixturePurchase struct {
	OfferingID string `json:"ReservedInstanceOfferingId"`
	Name       string `json:"ReservationName"`
	Count      int32  `json:"InstanceCount"`
}

type offeringFixture struct {
	client       provider.ServiceClient
	mu           sync.Mutex
	requests     int
	listTokens   []string
	detailIDs    []string
	purchases    []fixturePurchase
	dialAttempts atomic.Int32
}

func newOfferingFixture(t *testing.T, pages [][]fixtureOffering, selected fixtureOffering) *offeringFixture {
	t.Helper()
	f := &offeringFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == offeringPath:
			query := r.URL.Query()
			if _, ok := query["offeringId"]; ok {
				want := url.Values{"maxResults": {"1"}, "offeringId": {selected.ID}}
				if query.Encode() != want.Encode() {
					t.Errorf("details query = %q, want %q", query.Encode(), want.Encode())
					http.Error(w, "unexpected details query", http.StatusBadRequest)
					return
				}
				f.mu.Lock()
				f.detailIDs = append(f.detailIDs, query.Get("offeringId"))
				f.mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{"ReservedInstanceOfferings": []fixtureOffering{selected}})
				return
			}
			page := 0
			want := url.Values{"maxResults": {"100"}}
			if query.Get("nextToken") != "" {
				page = 1
				want.Set("nextToken", "page-2")
			}
			if query.Encode() != want.Encode() || page >= len(pages) {
				t.Errorf("list query = %q, page = %d, want %q within %d pages", query.Encode(), page, want.Encode(), len(pages))
				http.Error(w, "unexpected list query", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.listTokens = append(f.listTokens, query.Get("nextToken"))
			f.mu.Unlock()
			response := map[string]any{"ReservedInstanceOfferings": pages[page]}
			if page+1 < len(pages) {
				response["NextToken"] = "page-2"
			}
			_ = json.NewEncoder(w).Encode(response)
		case r.Method == http.MethodPost && r.URL.Path == purchasePath:
			if r.URL.RawQuery != "" {
				t.Errorf("purchase query = %q, want empty", r.URL.RawQuery)
				http.Error(w, "unexpected purchase query", http.StatusBadRequest)
				return
			}
			var purchase fixturePurchase
			if err := json.NewDecoder(r.Body).Decode(&purchase); err != nil {
				t.Errorf("decode purchase: %v", err)
				http.Error(w, "invalid purchase", http.StatusBadRequest)
				return
			}
			f.mu.Lock()
			f.purchases = append(f.purchases, purchase)
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{"ReservedInstanceId": "ri-synthetic"})
		default:
			t.Errorf("unexpected SDK request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	fixtureAddress := server.Listener.Addr().String()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		f.dialAttempts.Add(1)
		if network != "tcp" || address != fixtureAddress {
			return nil, fmt.Errorf("blocked SDK dial to %s %s", network, address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("blocked SDK redirect to %s", req.URL)
		},
	}
	t.Cleanup(func() {
		transport.CloseIdleConnections()
		server.Close()
	})
	f.client = opensearch.NewClient(aws.Config{
		Region:       "us-east-1",
		BaseEndpoint: aws.String(server.URL),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture", Source: "fixture"}, nil
		}),
		HTTPClient: httpClient,
	})
	return f
}

func (f *offeringFixture) snapshot() (int, []string, []string, []fixturePurchase) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, append([]string(nil), f.listTokens...), append([]string(nil), f.detailIDs...), append([]fixturePurchase(nil), f.purchases...)
}

func selectedOffering(id, payment string) fixtureOffering {
	return fixtureOffering{
		ID: id, InstanceType: "m5.large.search", Payment: payment, Duration: selectedDuration,
		FixedPrice: 123.5, UsagePrice: 0.25, Currency: "USD",
	}
}

func selectionRecommendation(payment string) common.Recommendation {
	return common.Recommendation{
		Service: common.ServiceSearch, Region: "us-east-1", ResourceType: "m5.large.search",
		Count: 3, Term: "1yr", PaymentOption: payment,
	}
}

func TestOpenSearchOfferingSelectionPurchase(t *testing.T) {
	for _, option := range []struct {
		slug string
		enum types.ReservedInstancePaymentOption
	}{
		{"all-upfront", types.ReservedInstancePaymentOptionAllUpfront},
		{"partial-upfront", types.ReservedInstancePaymentOptionPartialUpfront},
		{"no-upfront", types.ReservedInstancePaymentOptionNoUpfront},
	} {
		for _, placement := range []string{"same-page", "next-page"} {
			t.Run(option.slug+"/"+placement, func(t *testing.T) {
				selected := selectedOffering("selected-"+option.slug, string(option.enum))
				wrongPayment := selectedOffering("wrong-payment", string(types.ReservedInstancePaymentOptionNoUpfront))
				if option.enum == types.ReservedInstancePaymentOptionNoUpfront {
					wrongPayment.Payment = string(types.ReservedInstancePaymentOptionAllUpfront)
				}
				wrongType := selectedOffering("wrong-type", string(option.enum))
				wrongType.InstanceType = "m5.xlarge.search"
				wrongDuration := selectedOffering("wrong-duration", string(option.enum))
				wrongDuration.Duration = 94608000
				pages := [][]fixtureOffering{{wrongType, wrongDuration, wrongPayment, selected}}
				wantTokens := []string{""}
				if placement == "next-page" {
					pages = [][]fixtureOffering{{wrongType, wrongDuration, wrongPayment}, {selected}}
					wantTokens = []string{"", "page-2"}
				}
				fixture := newOfferingFixture(t, pages, selected)
				result, err := fixture.client.PurchaseCommitment(context.Background(), selectionRecommendation(option.slug), common.PurchaseOptions{})
				require.NoError(t, err)
				assert.True(t, result.Success)
				assert.Equal(t, "ri-synthetic", result.CommitmentID)
				requests, tokens, details, purchases := fixture.snapshot()
				assert.Equal(t, len(wantTokens)+1, requests)
				assert.Equal(t, wantTokens, tokens)
				assert.Empty(t, details)
				require.Len(t, purchases, 1)
				assert.Equal(t, selected.ID, purchases[0].OfferingID)
				assert.Equal(t, int32(3), purchases[0].Count)
				assert.NotEmpty(t, purchases[0].Name)
			})
		}
	}
}

func TestOpenSearchOfferingSelectionMatchFirstControl(t *testing.T) {
	selected := selectedOffering("first-match", string(types.ReservedInstancePaymentOptionAllUpfront))
	fixture := newOfferingFixture(t, [][]fixtureOffering{{selected}}, selected)
	result, err := fixture.client.PurchaseCommitment(context.Background(), selectionRecommendation("all-upfront"), common.PurchaseOptions{})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "ri-synthetic", result.CommitmentID)
	requests, tokens, _, purchases := fixture.snapshot()
	assert.Equal(t, 2, requests)
	assert.Equal(t, []string{""}, tokens)
	require.Len(t, purchases, 1)
	assert.Equal(t, "first-match", purchases[0].OfferingID)
	assert.Equal(t, int32(3), purchases[0].Count)
}

func TestOpenSearchOfferingSelectionExhaustedMismatches(t *testing.T) {
	selected := selectedOffering("never-returned", string(types.ReservedInstancePaymentOptionAllUpfront))
	wrong := selectedOffering("wrong-payment", string(types.ReservedInstancePaymentOptionNoUpfront))
	fixture := newOfferingFixture(t, [][]fixtureOffering{{wrong}, {wrong}}, selected)
	result, err := fixture.client.PurchaseCommitment(context.Background(), selectionRecommendation("all-upfront"), common.PurchaseOptions{})
	require.ErrorContains(t, err, "no offerings found")
	assert.False(t, result.Success)
	requests, tokens, _, purchases := fixture.snapshot()
	assert.Equal(t, 2, requests)
	assert.Equal(t, []string{"", "page-2"}, tokens)
	assert.Empty(t, purchases)
}

func TestOpenSearchOfferingSelectionInvalidPaymentNoHTTP(t *testing.T) {
	for _, payment := range []struct{ name, value string }{
		{"empty", ""}, {"unknown", "unknown"}, {"spaced", "All Upfront"}, {"raw-enum", "ALL_UPFRONT"},
	} {
		for _, method := range []string{"purchase", "validate", "details"} {
			t.Run(payment.name+"/"+method, func(t *testing.T) {
				selected := selectedOffering("invalid-payment-control", string(types.ReservedInstancePaymentOptionAllUpfront))
				fixture := newOfferingFixture(t, [][]fixtureOffering{{selected}}, selected)
				rec := selectionRecommendation(payment.value)
				var err error
				switch method {
				case "purchase":
					_, err = fixture.client.PurchaseCommitment(context.Background(), rec, common.PurchaseOptions{})
				case "validate":
					err = fixture.client.ValidateOffering(context.Background(), rec)
				case "details":
					_, err = fixture.client.GetOfferingDetails(context.Background(), rec)
				}
				assert.ErrorContains(t, err, "unsupported OpenSearch payment option")
				requests, _, _, _ := fixture.snapshot()
				assert.Zero(t, requests)
				assert.Zero(t, fixture.dialAttempts.Load())
			})
		}
	}
}

func TestOpenSearchOfferingSelectionValidateAndDetails(t *testing.T) {
	selected := selectedOffering("selected-for-details", string(types.ReservedInstancePaymentOptionPartialUpfront))
	wrong := selectedOffering("wrong-payment", string(types.ReservedInstancePaymentOptionNoUpfront))
	fixture := newOfferingFixture(t, [][]fixtureOffering{{wrong}, {selected}}, selected)
	rec := selectionRecommendation("partial-upfront")
	require.NoError(t, fixture.client.ValidateOffering(context.Background(), rec))
	details, err := fixture.client.GetOfferingDetails(context.Background(), rec)
	require.NoError(t, err)
	require.NotNil(t, details)
	assert.Equal(t, selected.ID, details.OfferingID)
	assert.Equal(t, selected.InstanceType, details.ResourceType)
	assert.Equal(t, selected.Payment, details.PaymentOption)
	assert.Equal(t, selected.FixedPrice, details.UpfrontCost)
	assert.Equal(t, selected.UsagePrice, details.RecurringCost)
	requests, tokens, detailIDs, purchases := fixture.snapshot()
	assert.Equal(t, 5, requests)
	assert.Equal(t, []string{"", "page-2", "", "page-2"}, tokens)
	assert.Equal(t, []string{selected.ID}, detailIDs)
	assert.Empty(t, purchases)
}
