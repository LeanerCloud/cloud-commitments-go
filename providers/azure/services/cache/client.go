// Package cache provides Azure Cache for Redis Reserved Capacity client
package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/redis/armredis/v3"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/httpclient"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
)

// maxReservationsPages caps reservation-detail pagination.
const maxReservationsPages = 50

// HTTPClient interface for HTTP operations (enables mocking).
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// ReservationsDetailsPager interface for reservations details pager (enables mocking).
type ReservationsDetailsPager interface {
	More() bool
	NextPage(ctx context.Context) (armconsumption.ReservationsDetailsClientListResponse, error)
}

// RedisCachesPager interface for Redis caches pager (enables mocking).
type RedisCachesPager interface {
	More() bool
	NextPage(ctx context.Context) (armredis.ClientListBySubscriptionResponse, error)
}

// Client handles Azure Cache for Redis Reserved Capacity.
type Client struct {
	cred              azcore.TokenCredential
	httpClient        HTTPClient
	reservationsPager ReservationsDetailsPager
	redisCachesPager  RedisCachesPager

	subscriptionID string
	region         string
}

// NewClient creates a new Azure Cache client.
func NewClient(cred azcore.TokenCredential, subscriptionID, region string) *Client {
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpclient.New(),
	}
}

// NewClientWithHTTP creates a new Azure Cache client with a custom HTTP client (for testing).
// When httpClient is nil, the SSRF-hardened httpclient.New() is used so the nil
// fallback also blocks IMDS connections. A non-nil httpClient is used as-is and
// bypasses that guard: metadata, NAT64/6to4 and unspecified addresses are
// dialable unless the caller's client blocks them itself.
func NewClientWithHTTP(cred azcore.TokenCredential, subscriptionID, region string, httpClient HTTPClient) *Client {
	if httpClient == nil {
		httpClient = httpclient.New()
	}
	return &Client{
		cred:           cred,
		subscriptionID: subscriptionID,
		region:         region,
		httpClient:     httpClient,
	}
}

// SetReservationsPager sets the reservations pager (for testing).
func (c *Client) SetReservationsPager(pager ReservationsDetailsPager) {
	c.reservationsPager = pager
}

// SetRedisCachesPager sets the Redis caches pager (for testing).
func (c *Client) SetRedisCachesPager(pager RedisCachesPager) {
	c.redisCachesPager = pager
}

// GetServiceType returns the service type.
func (c *Client) GetServiceType() common.ServiceType {
	return common.ServiceCache
}

// GetRegion returns the region.
func (c *Client) GetRegion() string {
	return c.region
}

// AzureRetailPrice is the response envelope for the Azure Retail Prices API.
type AzureRetailPrice = pricing.Page[pricing.RetailPriceItem]

// GetRecommendations returns an empty slice: Azure Redis recommendations are
// collected, labeled ServiceMemoryDB, by the managedredis client. Collecting
// them here too would list every Redis recommendation twice, and a
// ServiceCache label routes the purchase to a client that must not buy Redis.
func (c *Client) GetRecommendations(_ context.Context, _ *common.RecommendationParams) ([]common.Recommendation, error) {
	return []common.Recommendation{}, nil
}

// GetExistingCommitments retrieves existing Redis Cache reserved capacity.
func (c *Client) GetExistingCommitments(ctx context.Context) ([]common.Commitment, error) {
	pager, err := c.createReservationsPager()
	if err != nil {
		return nil, fmt.Errorf("cache: create reservations pager: %w", err)
	}

	return c.collectRedisReservations(ctx, pager)
}

// createReservationsPager creates a pager for listing reservations.
func (c *Client) createReservationsPager() (ReservationsDetailsPager, error) {
	// Use injected pager if available (for testing)
	if c.reservationsPager != nil {
		return c.reservationsPager, nil
	}

	client, err := armconsumption.NewReservationsDetailsClient(c.cred, nil)
	if err != nil {
		return nil, err
	}

	scope := fmt.Sprintf("subscriptions/%s", c.subscriptionID)
	return client.NewListPager(scope, &armconsumption.ReservationsDetailsClientListOptions{}), nil
}

// collectRedisReservations collects Redis reservations from the pager.
// Returns an error on first pagination failure so callers can't silently act
// on a partial list — see the compute client for the full rationale.
func (c *Client) collectRedisReservations(ctx context.Context, pager ReservationsDetailsPager) ([]common.Commitment, error) {
	commitments := make([]common.Commitment, 0)

	for pageIdx := 0; pager.More(); pageIdx++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("context canceled during pagination: %w", err)
		}
		if pageIdx >= maxReservationsPages {
			return nil, fmt.Errorf("cache: GetExistingCommitments pagination cap (%d pages) reached", maxReservationsPages)
		}
		page, err := slot.NextPage(ctx, pager)
		if err != nil {
			return nil, fmt.Errorf("cache: list reservations: %w", err)
		}

		for _, detail := range page.Value {
			if commitment := c.convertRedisReservation(detail); commitment != nil {
				commitments = append(commitments, *commitment)
			}
		}
	}

	// ReservationsDetails is a daily usage API: one row per reservation per
	// usage day. Without this, a single reservation held for N days would
	// surface as N commitments sharing one CommitmentID (issue #73).
	return reservations.DedupeCommitmentsByID(commitments), nil
}

// convertRedisReservation converts a reservation detail to a commitment if it's a Redis reservation.
func (c *Client) convertRedisReservation(detail *armconsumption.ReservationDetail) *common.Commitment {
	if detail.Properties == nil {
		return nil
	}

	props := detail.Properties
	// Filter for Redis reservations - check SKU name since ReservedResourceType may not be available
	if props.SKUName == nil || !strings.Contains(strings.ToLower(*props.SKUName), "redis") {
		return nil
	}

	commitment := &common.Commitment{
		Provider:       common.ProviderAzure,
		Account:        c.subscriptionID,
		CommitmentType: common.CommitmentReservedInstance,
		Service:        common.ServiceCache,
	}

	if props.ReservationID != nil {
		commitment.CommitmentID = *props.ReservationID
	}
	if props.SKUName != nil {
		commitment.ResourceType = *props.SKUName
	}

	return commitment
}

// PurchaseCommitment refuses: managedredis (ServiceMemoryDB) is the only Azure service that
// buys RedisCache reservations (#538), so one Redis recommendation cannot be bought twice.
func (c *Client) PurchaseCommitment(_ context.Context, rec common.Recommendation, _ common.PurchaseOptions) (common.PurchaseResult, error) {
	err := fmt.Errorf("%w: Azure Redis reservations are purchased through the %s service, not %s",
		common.ErrRefusedBeforePurchase, common.ServiceMemoryDB, common.ServiceCache)
	return common.PurchaseResult{Recommendation: rec, Timestamp: time.Now(), Error: err}, err
}

// GetOfferingDetails retrieves Redis Cache reservation offering details from Azure Retail Prices API.
func (c *Client) GetOfferingDetails(ctx context.Context, rec common.Recommendation) (*common.OfferingDetails, error) {
	termYears, err := reservations.ParseTermYears(rec.Term)
	if err != nil {
		return nil, fmt.Errorf("invalid term: %w", err)
	}

	redisPricing, err := c.getRedisPricing(ctx, rec.ResourceType, c.region, termYears)
	if err != nil {
		return nil, fmt.Errorf("failed to get pricing: %w", err)
	}

	var upfrontCost, recurringCost float64
	totalCost := redisPricing.ReservationPrice

	switch common.PaymentOption(rec.PaymentOption) {
	case common.PaymentAllUpfront, "upfront":
		upfrontCost = totalCost
		recurringCost = 0
	case "monthly", common.PaymentNoUpfront:
		upfrontCost = 0
		recurringCost = totalCost / (float64(termYears) * 12)
	default:
		// Fail loud on an unrecognized payment option rather than silently
		// billing it as all-upfront (owner policy: no silent fallbacks on
		// money-affecting fields).
		return nil, fmt.Errorf("unsupported payment option for Azure Cache for Redis offering details: %q", rec.PaymentOption)
	}

	return &common.OfferingDetails{
		OfferingID:          fmt.Sprintf("azure-redis-%s-%s-%s", rec.ResourceType, c.region, rec.Term),
		ResourceType:        rec.ResourceType,
		Term:                rec.Term,
		PaymentOption:       rec.PaymentOption,
		UpfrontCost:         upfrontCost,
		RecurringCost:       recurringCost,
		TotalCost:           totalCost,
		EffectiveHourlyRate: redisPricing.HourlyRate,
		Currency:            redisPricing.Currency,
	}, nil
}

// RedisPricing contains pricing information for Redis Cache.
type RedisPricing struct {
	Currency         string
	HourlyRate       float64
	ReservationPrice float64
}

func (c *Client) getRedisPricing(ctx context.Context, sku, region string, termYears int) (*RedisPricing, error) {
	identity, err := pricing.ParseRedisIdentity(sku)
	if err != nil {
		return nil, err
	}
	qRegion, err := pricing.ODataString(region)
	if err != nil {
		return nil, err
	}
	qSKU, err := pricing.ODataString(identity.ArmSKUName)
	if err != nil {
		return nil, err
	}
	filter := fmt.Sprintf("serviceName eq 'Redis Cache' and armRegionName eq %s and armSkuName eq %s and priceType eq 'Reservation'",
		qRegion, qSKU)

	params := url.Values{}
	params.Add("$filter", filter)
	params.Add("api-version", "2023-01-01-preview")

	initialURL := "https://prices.azure.com/api/retail/prices?" + params.Encode()
	items, err := pricing.FetchAll[pricing.RetailPriceItem](ctx, c.httpClient, initialURL, pricing.DefaultPageTimeout, pricing.DefaultMaxPages)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("no pricing data found for Redis Cache SKU %s in region %s", sku, region)
	}
	selected, err := pricing.SelectReservation(items, termYears, func(item pricing.RetailPriceItem) bool {
		return identity.Matches(item, region)
	})
	if err != nil {
		return nil, err
	}
	return &RedisPricing{
		HourlyRate:       selected.RetailPrice / (common.HoursPerYear * float64(termYears)),
		ReservationPrice: selected.RetailPrice,
		Currency:         selected.CurrencyCode,
	}, nil
}
