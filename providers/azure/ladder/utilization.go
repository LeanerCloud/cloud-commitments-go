// Package ladder holds the Azure side of the commitment ladder.
package ladder

// Savings plan utilization comes from the Cost Management benefit utilization
// summaries, list by savings plan order, api-version 2026-06-01:
// https://learn.microsoft.com/en-us/rest/api/cost-management/benefit-utilization-summaries/list-by-savings-plan-order?view=rest-cost-management-2026-06-01

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/httpclient"
)

const (
	armBaseURL             = "https://management.azure.com"
	utilizationAPIVersion  = "2026-06-01"
	utilizationGrain       = "Daily"
	maxUtilizationBodySize = 8 << 20
)

// UtilizationReader reads savings plan utilization from Cost Management.
type UtilizationReader struct {
	cred    azcore.TokenCredential
	httpc   *http.Client
	baseURL string
}

// NewUtilizationReader returns a reader that calls ARM through the SSRF-hardened client.
func NewUtilizationReader(cred azcore.TokenCredential) *UtilizationReader {
	return &UtilizationReader{cred: cred, httpc: httpclient.New(), baseURL: armBaseURL}
}

type utilizationPage struct {
	NextLink string `json:"nextLink"`
	Value    []struct {
		Properties struct {
			AvgUtilization *float64 `json:"avgUtilizationPercentage"`
			BenefitID      string   `json:"benefitId"`
		} `json:"properties"`
	} `json:"value"`
}

// GetSavingsPlanUtilization returns, per savings plan id, the mean of the daily
// average utilization percentages (0 to 100) over [start, end] for the given
// savings plan order ids. Orders with no summaries contribute nothing.
func (r *UtilizationReader) GetSavingsPlanUtilization(ctx context.Context, orderIDs []string, start, end time.Time) (map[string]float64, error) {
	token, err := r.cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{armBaseURL + "/.default"}})
	if err != nil {
		return nil, fmt.Errorf("azure savings plan utilization: get access token: %w", err)
	}
	sums := map[string]float64{}
	counts := map[string]int{}
	for _, orderID := range orderIDs {
		if err := r.readOrder(ctx, token.Token, orderID, start, end, sums, counts); err != nil {
			return nil, err
		}
	}
	out := make(map[string]float64, len(sums))
	for id, sum := range sums {
		out[id] = sum / float64(counts[id])
	}
	return out, nil
}

func (r *UtilizationReader) readOrder(ctx context.Context, token, orderID string, start, end time.Time, sums map[string]float64, counts map[string]int) error {
	q := url.Values{}
	q.Set("api-version", utilizationAPIVersion)
	q.Set("grainParameter", utilizationGrain)
	q.Set("$filter", fmt.Sprintf("properties/usageDate ge %s and properties/usageDate le %s",
		start.UTC().Format("2006-01-02"), end.UTC().Format("2006-01-02")))
	next := fmt.Sprintf("%s/providers/microsoft.BillingBenefits/savingsPlanOrders/%s/providers/Microsoft.CostManagement/benefitUtilizationSummaries?%s",
		r.baseURL, url.PathEscape(orderID), q.Encode())

	for next != "" {
		page, err := r.fetchPage(ctx, token, next)
		if err != nil {
			return fmt.Errorf("azure savings plan utilization for order %s: %w", orderID, err)
		}
		for _, v := range page.Value {
			id := v.Properties.BenefitID
			if id == "" {
				return fmt.Errorf("azure savings plan utilization for order %s: summary without benefitId", orderID)
			}
			if v.Properties.AvgUtilization == nil {
				return fmt.Errorf("azure savings plan utilization for plan %s: missing avgUtilizationPercentage", id)
			}
			pct := *v.Properties.AvgUtilization
			if pct < 0 || pct > 100 {
				return fmt.Errorf("azure savings plan utilization for plan %s: percentage %v out of range 0-100", id, pct)
			}
			sums[id] += pct
			counts[id]++
		}
		next = page.NextLink
		if next != "" && !strings.HasPrefix(next, r.baseURL+"/") {
			return fmt.Errorf("azure savings plan utilization for order %s: nextLink outside %s", orderID, r.baseURL)
		}
	}
	return nil
}

func (r *UtilizationReader) fetchPage(ctx context.Context, token, target string) (*utilizationPage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := r.httpc.Do(req) //nolint:gosec // target is the fixed ARM base URL or a nextLink checked against it; client is the SSRF guard
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUtilizationBodySize))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var page utilizationPage
	if err := json.Unmarshal(body, &page); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &page, nil
}
