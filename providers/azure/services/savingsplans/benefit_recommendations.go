package savingsplans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/recommendations"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/services/internal/reservations"
)

// Cost Management Benefit Recommendations - List, api-version 2024-08-01:
// https://learn.microsoft.com/rest/api/cost-management/benefit-recommendations/list?view=rest-cost-management-2024-08-01
//
// Scope: this client requests at subscription scope with
// properties/scope eq 'Shared', the same applied scope PurchaseCommitment
// uses. Billing-account and billing-profile scopes need an ID this client
// does not have, and Single-scope rows would need a per-subscription purchase
// path that does not exist.
const (
	benefitRecsAPIVersion = "2024-08-01"
	armHost               = "management.azure.com"
	armEndpoint           = "https://" + armHost
	benefitRecsMaxPages   = 50
	benefitRecsPageTO     = 30 * time.Second

	armSkuComputeSavingsPlan = "Compute_Savings_Plan"
	kindSavingsPlan          = "SavingsPlan"
	scopeShared              = "Shared"
	grainHourly              = "Hourly"

	// defaultBenefitLookback is sent when params carry no lookback, so the
	// scaling never depends on an API-side default. It matches the Azure
	// reservation path (internal/recommendations consumptionLookBack).
	defaultBenefitLookback = "Last7Days"

	// hoursPerMonth is the 730 h month Azure's own pricing pages use. The day
	// based reservation scaling (recommendations.DaysPerMonth) is unrelated.
	hoursPerMonth = common.HoursPerMonth
)

type benefitRecsPage struct {
	Value    []benefitRecommendation `json:"value"`
	NextLink string                  `json:"nextLink"`
}

type benefitRecommendation struct {
	ID         string                      `json:"id"`
	Kind       string                      `json:"kind"`
	Properties *benefitRecommendationProps `json:"properties"`
}

type benefitRecommendationProps struct {
	ArmSkuName            string                 `json:"armSkuName"`
	Term                  string                 `json:"term"`
	Scope                 string                 `json:"scope"`
	CommitmentGranularity string                 `json:"commitmentGranularity"`
	CurrencyCode          string                 `json:"currencyCode"`
	TotalHours            *int32                 `json:"totalHours"`
	CostWithoutBenefit    *float64               `json:"costWithoutBenefit"`
	RecommendationDetails *benefitRecommendDetls `json:"recommendationDetails"`
}

type benefitRecommendDetls struct {
	CommitmentAmount *float64 `json:"commitmentAmount"`
	SavingsAmount    *float64 `json:"savingsAmount"`
	SavingsPct       *float64 `json:"savingsPercentage"`
}

// benefitLookback maps RecommendationParams.LookbackPeriod to the API enum.
// An empty value selects defaultBenefitLookback.
func benefitLookback(p string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case "", "7d":
		return defaultBenefitLookback, nil
	case "30d":
		return "Last30Days", nil
	case "60d":
		return "Last60Days", nil
	default:
		return "", fmt.Errorf("unsupported lookback period %q for Azure savings plan recommendations (want 7d, 30d or 60d)", p)
	}
}

// benefitTerms returns the API terms to request. An empty term requests both
// P1Y and P3Y because the API default (P3Y) would silently drop 1-year rows.
func benefitTerms(t string) ([]string, error) {
	if strings.TrimSpace(t) == "" {
		return []string{"P1Y", "P3Y"}, nil
	}
	years, err := reservations.ParseTermYears(t)
	if err != nil {
		return nil, fmt.Errorf("unsupported term %q for Azure savings plan recommendations (the API supports 1yr and 3yr): %w", t, err)
	}
	return []string{fmt.Sprintf("P%dY", years)}, nil
}

func (c *Client) benefitRecsURL(term, lookback string) string {
	filter := "properties/scope eq 'Shared' AND properties/term eq '" + term +
		"' AND properties/lookBackPeriod eq '" + lookback + "'"
	q := url.Values{}
	q.Set("$filter", filter)
	q.Set("api-version", benefitRecsAPIVersion)
	return fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.CostManagement/benefitRecommendations?%s",
		armEndpoint, url.PathEscape(c.subscriptionID), q.Encode())
}

// getBenefitRecommendations fetches Shared-scope Compute Savings Plan
// recommendations for the client's subscription.
func (c *Client) getBenefitRecommendations(ctx context.Context, params *common.RecommendationParams) ([]common.Recommendation, error) {
	if params == nil {
		params = &common.RecommendationParams{}
	}
	if c.cred == nil {
		return nil, fmt.Errorf("azure savings plan recommendations require a credential")
	}
	lookback, err := benefitLookback(params.LookbackPeriod)
	if err != nil {
		return nil, err
	}
	terms, err := benefitTerms(params.Term)
	if err != nil {
		return nil, err
	}
	token, err := c.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{armEndpoint + "/.default"}})
	if err != nil {
		return nil, fmt.Errorf("failed to get access token: %w", err)
	}

	recs := make([]common.Recommendation, 0)
	for _, term := range terms {
		rows, err := c.fetchBenefitPages(ctx, token.Token, c.benefitRecsURL(term, lookback))
		if err != nil {
			return nil, fmt.Errorf("azure savings plan recommendations (term %s): %w", term, err)
		}
		converted, err := c.convertBenefitRows(ctx, rows, term)
		if err != nil {
			return nil, err
		}
		recs = append(recs, converted...)
	}
	return recs, nil
}

// convertBenefitRows maps rows to recommendations. A non-USD row is recorded in
// the context's SkipLog and dropped; any other invalid row fails the call.
func (c *Client) convertBenefitRows(ctx context.Context, rows []benefitRecommendation, term string) ([]common.Recommendation, error) {
	recs := make([]common.Recommendation, 0, len(rows))
	for i := range rows {
		rec, err := convertBenefitRecommendation(&rows[i], c.subscriptionID)
		if errors.Is(err, recommendations.ErrNotUSD) {
			recommendations.RecordSkip(ctx, "savingsplans", fmt.Errorf("subscription %s recommendation %q (term %s): %w", c.subscriptionID, rows[i].ID, term, err))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("azure savings plan recommendation %q (term %s): %w", rows[i].ID, term, err)
		}
		recs = append(recs, *rec)
	}
	return recs, nil
}

func (c *Client) fetchBenefitPages(ctx context.Context, bearer, firstURL string) ([]benefitRecommendation, error) {
	var all []benefitRecommendation
	next := firstURL
	for pageIdx := 0; next != ""; pageIdx++ {
		if pageIdx >= benefitRecsMaxPages {
			return nil, fmt.Errorf("result exceeds the %d-page cap with nextLink still set", benefitRecsMaxPages)
		}
		if err := requireARMURL(next); err != nil {
			return nil, fmt.Errorf("page %d: %w", pageIdx, err)
		}
		page, err := slot.Do(ctx, func() (*benefitRecsPage, error) {
			return c.fetchBenefitPage(ctx, bearer, next, pageIdx)
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Value...)
		next = page.NextLink
	}
	return all, nil
}

// requireARMURL stops the bearer token being sent to a host the response
// named, since nextLink comes from the server.
func requireARMURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, armHost) {
		return fmt.Errorf("refusing to follow link outside https://%s", armHost)
	}
	return nil
}

func (c *Client) fetchBenefitPage(ctx context.Context, bearer, pageURL string, pageIdx int) (*benefitRecsPage, error) {
	pageCtx, cancel := context.WithTimeout(ctx, benefitRecsPageTO)
	defer cancel()
	req, err := http.NewRequestWithContext(pageCtx, http.MethodGet, pageURL, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("page %d: build request: %w", pageIdx, err)
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("page %d: %w", pageIdx, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("page %d: read body: %w", pageIdx, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("page %d: status %d: %s", pageIdx, resp.StatusCode, string(body))
	}
	var page benefitRecsPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("page %d: decode: %w", pageIdx, err)
	}
	return &page, nil
}

func termFromAPI(t string) (string, error) {
	switch t {
	case "P1Y":
		return "1yr", nil
	case "P3Y":
		return "3yr", nil
	default:
		return "", fmt.Errorf("unknown term %q", t)
	}
}

// convertBenefitRecommendation maps one API row to a Recommendation. The API
// reports cost and savings over totalHours of look-back usage; they are
// scaled to a 730 h month. Savings plans bill monthly with no upfront payment
// (CommitmentCost 0; RecurringMonthlyCost = hourly commitment x 730).
func convertBenefitRecommendation(r *benefitRecommendation, subscriptionID string) (*common.Recommendation, error) {
	term, err := validateBenefitRow(r)
	if err != nil {
		return nil, err
	}
	p, d := r.Properties, r.Properties.RecommendationDetails
	monthly := hoursPerMonth / float64(*p.TotalHours)
	recurring := *d.CommitmentAmount * hoursPerMonth
	rec := &common.Recommendation{
		Provider:             common.ProviderAzure,
		Account:              subscriptionID,
		Service:              common.ServiceSavingsPlansAll,
		ResourceType:         computeResourceType,
		Count:                1,
		CommitmentType:       common.CommitmentSavingsPlan,
		Term:                 term,
		PaymentOption:        "monthly",
		OnDemandCost:         *p.CostWithoutBenefit * monthly,
		CommitmentCost:       0,
		EstimatedSavings:     *d.SavingsAmount * monthly,
		RecurringMonthlyCost: &recurring,
		SourceRecommendation: r.ID,
		Timestamp:            time.Now(),
		Details: &common.SavingsPlanDetails{
			PlanType:         "Compute",
			HourlyCommitment: *d.CommitmentAmount,
			Currency:         strings.ToUpper(p.CurrencyCode),
		},
	}
	if d.SavingsPct != nil {
		rec.SavingsPercentage = *d.SavingsPct
	}
	return rec, nil
}

// validateBenefitRow rejects any row whose identity or numbers cannot be
// mapped faithfully, and returns the CUDly term.
func validateBenefitRow(r *benefitRecommendation) (string, error) {
	p := r.Properties
	switch {
	case r.Kind != kindSavingsPlan:
		return "", fmt.Errorf("unexpected kind %q", r.Kind)
	case p == nil || p.RecommendationDetails == nil:
		return "", fmt.Errorf("missing properties.recommendationDetails")
	case p.ArmSkuName != armSkuComputeSavingsPlan:
		return "", fmt.Errorf("unknown armSkuName %q", p.ArmSkuName)
	case p.Scope != scopeShared:
		return "", fmt.Errorf("unexpected scope %q (only Shared is requested)", p.Scope)
	case p.CommitmentGranularity != grainHourly:
		return "", fmt.Errorf("unsupported commitmentGranularity %q", p.CommitmentGranularity)
	case !currencyCodePattern.MatchString(p.CurrencyCode):
		return "", fmt.Errorf("currencyCode %q is not a 3-letter code", p.CurrencyCode)
	}
	if err := recommendations.RequireUSDCode(p.CurrencyCode); err != nil {
		return "", err
	}
	if err := validateBenefitNumbers(p); err != nil {
		return "", err
	}
	return termFromAPI(p.Term)
}

func validateBenefitNumbers(p *benefitRecommendationProps) error {
	d := p.RecommendationDetails
	switch {
	case d.CommitmentAmount == nil || *d.CommitmentAmount <= 0:
		return fmt.Errorf("missing or non-positive commitmentAmount")
	case d.SavingsAmount == nil || p.CostWithoutBenefit == nil:
		return fmt.Errorf("missing savingsAmount or costWithoutBenefit")
	case p.TotalHours == nil || *p.TotalHours <= 0:
		return fmt.Errorf("missing or non-positive totalHours")
	}
	return nil
}
