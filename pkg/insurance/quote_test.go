package insurance

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Fixtures are hand-built from the documented schemas in
// docs.archera.ai/api-reference/public-api/commitment-plans.md. Numbers are
// synthetic.

const financialsJSON = `{
	"commitment_cost": {"total": 110, "breakdown": {"cloud_provider_cost": {"total": 100}, "archera_premium": 10}},
	"commitment_savings": {"net": -5.5, "gross": 4.5},
	"covered_ondemand_cost": 104.5
}`

func offerEntryJSON(isCurrent bool, lease, term, payment string) string {
	return `{
		"is_current": ` + boolJSON(isCurrent) + `,
		"offer_id": "11111111-1111-4111-8111-111111111111",
		"offer_org_id": "public",
		"offer": {"provider": "aws", "type": "aws/AmazonEC2", "region": "us-east-1", "guaranteed_display_name": null},
		"lease_menu_item_id": ` + lease + `,
		"selected_amount": 3,
		"commitment_type": "aws/AmazonEC2",
		"contract_term": ` + term + `,
		"payment_option": ` + payment + `,
		"discount_rate": 0.3,
		"breakeven_days": null,
		"commitment_upfront_cost": 1200,
		"commitment_financials_monthly_rate": ` + financialsJSON + `,
		"delta_vs_current": {"monthly_net_savings": 0, "upfront_cost": 0, "discount_rate": 0, "breakeven_days": null}
	}`
}

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func validComparisonJSON() string {
	return `{
		"current_totals": {"commitment_financials_monthly_rate": ` + financialsJSON + `, "commitment_upfront_cost": 1200},
		"hypothetical_totals": [{
			"contract_term": null,
			"payment_option": "no_upfront",
			"commitment_financials_monthly_rate": {},
			"commitment_upfront_cost": 0,
			"delta_vs_current": {"monthly_net_savings": 7.25, "monthly_commitment_cost": -1, "upfront_cost": -1200},
			"line_items": [{
				"line_item_id": "22222222-2222-4222-8222-222222222222",
				"actual_term": null,
				"actual_payment_option": null,
				"actual_commitment_type": null,
				"actual_term_reason": "no_alternative"
			}]
		}],
		"data": [{
			"line_item_id": "22222222-2222-4222-8222-222222222222",
			"current": ` + offerEntryJSON(true, "null", "null", "null") + `,
			"candidates": [` + offerEntryJSON(false, `"33333333-3333-4333-8333-333333333333"`, `"one_year_gris"`, `"no_upfront"`) + `]
		}]
	}`
}

func decodeComparison(t *testing.T, body string) (*Comparison, error) {
	t.Helper()
	return DecodeComparison(strings.NewReader(body), "org", "plan", time.Unix(0, 0))
}

func rat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	require.True(t, ok)
	return r
}

func TestDecodeComparison_Golden(t *testing.T) {
	c, err := decodeComparison(t, validComparisonJSON())
	require.NoError(t, err)

	assert.Equal(t, "org", c.OrgID)
	assert.Equal(t, "plan", c.PlanID)
	assert.Nil(t, c.Currency, "no currency field is documented")

	// Premium is already inside the total; values pass through verbatim.
	m := c.Current.Monthly
	assert.Equal(t, rat(t, "110"), m.CommitmentCostTotal)
	assert.Equal(t, rat(t, "100"), m.CloudProviderCost)
	assert.Equal(t, rat(t, "10"), m.Premium)
	assert.Equal(t, rat(t, "-5.5"), m.NetSavings)
	assert.Equal(t, rat(t, "4.5"), m.GrossSavings)
	assert.Equal(t, rat(t, "1200"), c.Current.UpfrontCost)

	require.Len(t, c.Hypotheticals, 1)
	h := c.Hypotheticals[0]
	assert.Nil(t, h.ContractTerm, "null contract_term stays nil")
	assert.Equal(t, PaymentNoUpfront, h.PaymentOption)
	assert.Nil(t, h.Totals.Monthly.CommitmentCostTotal, "absent nested money is unknown, not zero")
	assert.Equal(t, rat(t, "0"), h.Totals.UpfrontCost)
	assert.Equal(t, rat(t, "7.25"), h.DeltaMonthlyNetSavings)
	require.Len(t, h.LineItems, 1)
	li := h.LineItems[0]
	assert.Nil(t, li.ActualTerm)
	assert.Nil(t, li.ActualPaymentOption)
	assert.Nil(t, li.ActualCommitmentType)
	assert.Equal(t, TermReasonNoAlternative, li.Reason)

	require.Len(t, c.Rows, 1)
	row := c.Rows[0]
	assert.Equal(t, "22222222-2222-4222-8222-222222222222", row.LineItemID)
	assert.True(t, row.Current.IsCurrent)
	assert.False(t, row.Current.LeaseBacked())
	assert.Nil(t, row.Current.ContractTerm)
	assert.Nil(t, row.Current.PaymentOption)
	assert.Nil(t, row.Current.BreakevenDays)
	assert.Equal(t, common.ProviderAWS, row.Current.Provider)

	require.Len(t, row.Candidates, 1, "candidates are preserved")
	cand := row.Candidates[0]
	assert.False(t, cand.IsCurrent)
	assert.True(t, cand.LeaseBacked())
	require.NotNil(t, cand.ContractTerm)
	assert.Equal(t, "one_year_gris", *cand.ContractTerm)
	require.NotNil(t, cand.PaymentOption)
	assert.Equal(t, PaymentNoUpfront, *cand.PaymentOption)
	assert.Equal(t, rat(t, "10"), cand.Monthly.Premium)
	assert.Equal(t, rat(t, "0"), cand.Delta.MonthlyNetSavings)
	assert.Nil(t, cand.Delta.BreakevenDays)
}

func TestDecodeComparison_EmptyArraysAreValid(t *testing.T) {
	c, err := decodeComparison(t, `{
		"current_totals": {"commitment_financials_monthly_rate": {}, "commitment_upfront_cost": 0},
		"hypothetical_totals": [], "data": []}`)
	require.NoError(t, err)
	assert.Empty(t, c.Hypotheticals)
	assert.NotNil(t, c.Hypotheticals)
	assert.Empty(t, c.Rows)
	assert.NotNil(t, c.Rows)
}

func TestDecodeComparison_IgnoresUnknownVendorFields(t *testing.T) {
	body := strings.Replace(validComparisonJSON(), `"current_totals"`, `"future_field": 1, "current_totals"`, 1)
	_, err := decodeComparison(t, body)
	require.NoError(t, err)
}

// mutate decodes the valid fixture, applies edit, and re-encodes it, so each
// negative case differs from a known-good body by exactly one change.
func mutate(t *testing.T, edit func(m map[string]any)) string {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(validComparisonJSON()), &m))
	edit(m)
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func obj(v any) map[string]any   { return v.(map[string]any) }
func first(v any) map[string]any { return obj(v.([]any)[0]) }

func TestDecodeComparison_RejectsContractBreaks(t *testing.T) {
	row := func(m map[string]any) map[string]any { return first(m["data"]) }
	cand := func(m map[string]any) map[string]any { return first(row(m)["candidates"]) }
	hyp := func(m map[string]any) map[string]any { return first(m["hypothetical_totals"]) }

	cases := map[string]string{
		"null body":           `null`,
		"empty object":        `{}`,
		"array body":          `[]`,
		"trailing JSON value": validComparisonJSON() + ` {"extra":1}`,
		"trailing garbage":    validComparisonJSON() + ` x`,
		"missing current_totals": mutate(t, func(m map[string]any) {
			delete(m, "current_totals")
		}),
		"null hypothetical_totals": mutate(t, func(m map[string]any) {
			m["hypothetical_totals"] = nil
		}),
		"missing data": mutate(t, func(m map[string]any) { delete(m, "data") }),
		"missing totals upfront": mutate(t, func(m map[string]any) {
			delete(obj(m["current_totals"]), "commitment_upfront_cost")
		}),
		"missing totals financials": mutate(t, func(m map[string]any) {
			delete(obj(m["current_totals"]), "commitment_financials_monthly_rate")
		}),
		"missing candidates": mutate(t, func(m map[string]any) { delete(row(m), "candidates") }),
		"missing current":    mutate(t, func(m map[string]any) { delete(row(m), "current") }),
		"empty line_item_id": mutate(t, func(m map[string]any) { row(m)["line_item_id"] = "" }),
		"null offer_id":      mutate(t, func(m map[string]any) { cand(m)["offer_id"] = nil }),
		"missing is_current": mutate(t, func(m map[string]any) { delete(cand(m), "is_current") }),
		"missing offer":      mutate(t, func(m map[string]any) { delete(cand(m), "offer") }),
		"missing commitment_type": mutate(t, func(m map[string]any) {
			delete(cand(m), "commitment_type")
		}),
		"null discount_rate": mutate(t, func(m map[string]any) { cand(m)["discount_rate"] = nil }),
		"missing entry delta": mutate(t, func(m map[string]any) {
			delete(cand(m), "delta_vs_current")
		}),
		"missing entry delta discount": mutate(t, func(m map[string]any) {
			delete(obj(cand(m)["delta_vs_current"]), "discount_rate")
		}),
		"unsupported payment": mutate(t, func(m map[string]any) {
			cand(m)["payment_option"] = "No Upfront"
		}),
		"unsupported term": mutate(t, func(m map[string]any) { cand(m)["contract_term"] = "forever" }),
		"unsupported provider": mutate(t, func(m map[string]any) {
			obj(cand(m)["offer"])["provider"] = "oracle"
		}),
		"malformed money": mutate(t, func(m map[string]any) {
			obj(obj(cand(m)["commitment_financials_monthly_rate"])["commitment_cost"])["total"] = "abc"
		}),
		"missing hypothetical payment": mutate(t, func(m map[string]any) {
			delete(hyp(m), "payment_option")
		}),
		"missing hypothetical delta": mutate(t, func(m map[string]any) {
			delete(hyp(m), "delta_vs_current")
		}),
		"null hypothetical delta upfront": mutate(t, func(m map[string]any) {
			obj(hyp(m)["delta_vs_current"])["upfront_cost"] = nil
		}),
		"missing hypothetical line_items": mutate(t, func(m map[string]any) {
			delete(hyp(m), "line_items")
		}),
		"unsupported term reason": mutate(t, func(m map[string]any) {
			first(hyp(m)["line_items"])["actual_term_reason"] = "guessed"
		}),
		"missing term reason": mutate(t, func(m map[string]any) {
			delete(first(hyp(m)["line_items"]), "actual_term_reason")
		}),
		"unsupported hypothetical term": mutate(t, func(m map[string]any) {
			hyp(m)["contract_term"] = "forever"
		}),
		"unsupported actual_term": mutate(t, func(m map[string]any) {
			first(hyp(m)["line_items"])["actual_term"] = "forever"
		}),
		"quoted entry discount_rate": mutate(t, func(m map[string]any) {
			cand(m)["discount_rate"] = "0.3"
		}),
		"quoted nested money": mutate(t, func(m map[string]any) {
			obj(obj(cand(m)["commitment_financials_monthly_rate"])["commitment_cost"])["total"] = "110"
		}),
		"quoted hypothetical delta": mutate(t, func(m map[string]any) {
			obj(hyp(m)["delta_vs_current"])["monthly_net_savings"] = "7"
		}),
		"boolean as number": mutate(t, func(m map[string]any) {
			cand(m)["discount_rate"] = true
		}),
		"unsupported actual payment": mutate(t, func(m map[string]any) {
			first(hyp(m)["line_items"])["actual_payment_option"] = "monthly"
		}),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			c, err := decodeComparison(t, body)
			assert.Error(t, err)
			assert.Nil(t, c)
		})
	}
}

func TestDecodeComparison_AbsentOfferProviderIsUnknown(t *testing.T) {
	body := mutate(t, func(m map[string]any) {
		delete(obj(first(first(m["data"])["candidates"])["offer"]), "provider")
	})
	c, err := decodeComparison(t, body)
	require.NoError(t, err)
	assert.Equal(t, common.ProviderType(""), c.Rows[0].Candidates[0].Provider)
}

const validPlanJSON = `{
	"id": "44444444-4444-4444-8444-444444444444", "name": "p", "status": "reviewed",
	"is_calculating": false, "fee_hourly": 0.25, "savings_hourly": 1.5,
	"commitment_coverage": 0.8, "monthly_savings": 1095
}`

func TestDecodePlan(t *testing.T) {
	p, err := DecodePlan(strings.NewReader(validPlanJSON))
	require.NoError(t, err)
	assert.Equal(t, "44444444-4444-4444-8444-444444444444", p.ID)
	assert.Equal(t, PlanStatusReviewed, p.Status)
	assert.False(t, p.IsCalculating)
	assert.Equal(t, rat(t, "0.25"), p.FeeHourly)
	assert.Equal(t, rat(t, "1.5"), p.SavingsHourly)
	assert.Equal(t, rat(t, "0.8"), p.CommitmentCoverage, "verbatim, no percent conversion")
	assert.Equal(t, rat(t, "1095"), p.MonthlySavings)
}

func TestDecodePlan_IsCalculating(t *testing.T) {
	body := strings.Replace(validPlanJSON, `"is_calculating": false`, `"is_calculating": true`, 1)
	require.NotEqual(t, validPlanJSON, body)
	p, err := DecodePlan(strings.NewReader(body))
	require.NoError(t, err)
	assert.True(t, p.IsCalculating)
}

func TestDecodePlan_RejectsContractBreaks(t *testing.T) {
	edit := func(field string, v any) string {
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(validPlanJSON), &m))
		if v == nil {
			delete(m, field)
		} else {
			m[field] = v
		}
		b, err := json.Marshal(m)
		require.NoError(t, err)
		return string(b)
	}
	cases := map[string]string{
		"null body":                   `null`,
		"empty object":                `{}`,
		"trailing JSON":               validPlanJSON + `{}`,
		"missing id":                  edit("id", nil),
		"missing status":              edit("status", nil),
		"unsupported status":          edit("status", "active"),
		"missing is_calculating":      edit("is_calculating", nil),
		"missing fee_hourly":          edit("fee_hourly", nil),
		"missing savings_hourly":      edit("savings_hourly", nil),
		"missing commitment_coverage": edit("commitment_coverage", nil),
		"quoted fee_hourly":           edit("fee_hourly", "0.25"),
		"missing monthly_savings":     edit("monthly_savings", nil),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := DecodePlan(strings.NewReader(body))
			assert.Error(t, err)
			assert.Nil(t, p)
		})
	}
}

func finJSON(base int) string {
	return fmt.Sprintf(`{
		"commitment_cost": {"total": %d, "breakdown": {"cloud_provider_cost": {"total": %d}, "archera_premium": %d}},
		"commitment_savings": {"net": %d, "gross": %d},
		"covered_ondemand_cost": %d}`, base+10, base+20, base+30, base+40, base+50, base+60)
}

func assertFin(t *testing.T, base int, f Financials) {
	t.Helper()
	assert.Equal(t, rat(t, fmt.Sprint(base+10)), f.CommitmentCostTotal, "total")
	assert.Equal(t, rat(t, fmt.Sprint(base+20)), f.CloudProviderCost, "cloud provider cost")
	assert.Equal(t, rat(t, fmt.Sprint(base+30)), f.Premium, "premium")
	assert.Equal(t, rat(t, fmt.Sprint(base+40)), f.NetSavings, "net")
	assert.Equal(t, rat(t, fmt.Sprint(base+50)), f.GrossSavings, "gross")
	assert.Equal(t, rat(t, fmt.Sprint(base+60)), f.CoveredOnDemandCost, "covered on-demand")
}

// Every mapped field carries a distinct non-zero value so a swapped or
// dropped field assignment changes at least one assertion.
func TestDecodeComparison_DistinctValuesForEveryMappedField(t *testing.T) {
	body := `{
		"current_totals": {"commitment_financials_monthly_rate": ` + finJSON(100) + `, "commitment_upfront_cost": 1111},
		"hypothetical_totals": [{
			"contract_term": "three_year",
			"payment_option": "partial_upfront",
			"commitment_financials_monthly_rate": ` + finJSON(200) + `,
			"commitment_upfront_cost": 2222,
			"delta_vs_current": {"monthly_net_savings": 7.25, "monthly_commitment_cost": -3.5, "upfront_cost": -1200.5},
			"line_items": [{
				"line_item_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
				"actual_term": "one_year_gris",
				"actual_payment_option": "all_upfront",
				"actual_commitment_type": "azure/Virtual Machines",
				"actual_term_reason": "fallback_closest_shorter"
			}]
		}],
		"data": [{
			"line_item_id": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			"current": {
				"is_current": true, "offer_id": "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
				"offer": {"provider": "gcp", "region": "europe-west4", "guaranteed_display_name": "Current G"},
				"lease_menu_item_id": null, "commitment_type": "gcp/cud",
				"contract_term": "three_year", "payment_option": "all_upfront",
				"discount_rate": 0.21, "breakeven_days": 33.5, "commitment_upfront_cost": 3333,
				"commitment_financials_monthly_rate": ` + finJSON(300) + `,
				"delta_vs_current": {"monthly_net_savings": 0, "upfront_cost": 0, "discount_rate": 0, "breakeven_days": null}
			},
			"candidates": [{
				"is_current": false, "offer_id": "dddddddd-dddd-4ddd-8ddd-dddddddddddd",
				"offer": {"provider": "azure", "region": "eu-west-1", "guaranteed_display_name": "Cand G"},
				"lease_menu_item_id": "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "commitment_type": "azure/Virtual Machines",
				"contract_term": "one_year_gris", "payment_option": "partial_upfront",
				"discount_rate": 0.31, "breakeven_days": 45.5, "commitment_upfront_cost": 4444,
				"commitment_financials_monthly_rate": ` + finJSON(400) + `,
				"delta_vs_current": {"monthly_net_savings": 1.5, "upfront_cost": 2.5, "discount_rate": 0.04, "breakeven_days": 6.5}
			}]
		}]
	}`
	c, err := decodeComparison(t, body)
	require.NoError(t, err)

	assertFin(t, 100, c.Current.Monthly)
	assert.Equal(t, rat(t, "1111"), c.Current.UpfrontCost)

	require.Len(t, c.Hypotheticals, 1)
	h := c.Hypotheticals[0]
	require.NotNil(t, h.ContractTerm)
	assert.Equal(t, "three_year", *h.ContractTerm)
	assert.Equal(t, PaymentPartialUpfront, h.PaymentOption)
	assertFin(t, 200, h.Totals.Monthly)
	assert.Equal(t, rat(t, "2222"), h.Totals.UpfrontCost)
	assert.Equal(t, rat(t, "7.25"), h.DeltaMonthlyNetSavings)
	assert.Equal(t, rat(t, "-3.5"), h.DeltaMonthlyCommitmentCost)
	assert.Equal(t, rat(t, "-1200.5"), h.DeltaUpfrontCost)
	require.Len(t, h.LineItems, 1)
	li := h.LineItems[0]
	assert.Equal(t, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", li.LineItemID)
	require.NotNil(t, li.ActualTerm)
	assert.Equal(t, "one_year_gris", *li.ActualTerm)
	require.NotNil(t, li.ActualPaymentOption)
	assert.Equal(t, PaymentAllUpfront, *li.ActualPaymentOption)
	require.NotNil(t, li.ActualCommitmentType)
	assert.Equal(t, "azure/Virtual Machines", *li.ActualCommitmentType)
	assert.Equal(t, TermReasonFallbackShorter, li.Reason)

	require.Len(t, c.Rows, 1)
	row := c.Rows[0]
	assert.Equal(t, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", row.LineItemID)

	cur := row.Current
	assert.True(t, cur.IsCurrent)
	assert.Equal(t, "cccccccc-cccc-4ccc-8ccc-cccccccccccc", cur.OfferID)
	assert.Equal(t, "gcp/cud", cur.CommitmentType)
	assert.Equal(t, common.ProviderGCP, cur.Provider)
	require.NotNil(t, cur.Region)
	assert.Equal(t, "europe-west4", *cur.Region)
	require.NotNil(t, cur.GuaranteedDisplayName)
	assert.Equal(t, "Current G", *cur.GuaranteedDisplayName)
	assert.Nil(t, cur.LeaseMenuItemID)
	assert.False(t, cur.LeaseBacked())
	assert.Equal(t, rat(t, "0.21"), cur.DiscountRate)
	assert.Equal(t, rat(t, "33.5"), cur.BreakevenDays)
	assert.Equal(t, rat(t, "3333"), cur.UpfrontCost)
	assertFin(t, 300, cur.Monthly)

	require.Len(t, row.Candidates, 1)
	cand := row.Candidates[0]
	assert.False(t, cand.IsCurrent)
	assert.Equal(t, "dddddddd-dddd-4ddd-8ddd-dddddddddddd", cand.OfferID)
	assert.Equal(t, "azure/Virtual Machines", cand.CommitmentType)
	assert.Equal(t, common.ProviderAzure, cand.Provider)
	require.NotNil(t, cand.Region)
	assert.Equal(t, "eu-west-1", *cand.Region)
	require.NotNil(t, cand.GuaranteedDisplayName)
	assert.Equal(t, "Cand G", *cand.GuaranteedDisplayName)
	require.NotNil(t, cand.LeaseMenuItemID)
	assert.Equal(t, "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", *cand.LeaseMenuItemID)
	assert.True(t, cand.LeaseBacked())
	require.NotNil(t, cand.ContractTerm)
	assert.Equal(t, "one_year_gris", *cand.ContractTerm)
	require.NotNil(t, cand.PaymentOption)
	assert.Equal(t, PaymentPartialUpfront, *cand.PaymentOption)
	assert.Equal(t, rat(t, "0.31"), cand.DiscountRate)
	assert.Equal(t, rat(t, "45.5"), cand.BreakevenDays)
	assert.Equal(t, rat(t, "4444"), cand.UpfrontCost)
	assertFin(t, 400, cand.Monthly)
	assert.Equal(t, rat(t, "1.5"), cand.Delta.MonthlyNetSavings)
	assert.Equal(t, rat(t, "2.5"), cand.Delta.UpfrontCost)
	assert.Equal(t, rat(t, "0.04"), cand.Delta.DiscountRate)
	assert.Equal(t, rat(t, "6.5"), cand.Delta.BreakevenDays)
}
