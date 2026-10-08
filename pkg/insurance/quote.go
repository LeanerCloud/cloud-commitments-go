package insurance

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/exchange"
)

// This file decodes the documented Archera API schemas into the shared types.
// It is a pure parser: vendor totals pass through verbatim (premium already
// included), and no financial value is recomputed.
//
// Decoded schemas (docs.archera.ai/api-reference/public-api/commitment-plans.md):
//   - LineItemOfferComparisonResponse (GET .../comparison)
//   - CommitmentPlan (GET .../commitment-plans/{plan_id})
//
// Validation is limited to what the mapper consumes: a field the schema marks
// required and non-nullable is rejected when absent or null; a required but
// nullable field maps null (or absent) to nil; enums a consumer branches on are
// checked. Unknown vendor properties are ignored so additive API changes do
// not break reads. Known limit: for required-but-nullable fields, an absent
// key and an explicit null both map to nil. Numbers must be JSON numbers; a
// quoted numeric string is a contract break.

var errMissing = errors.New("required field missing or null")

// wireNum holds the raw text of a JSON value that must be a number. Unlike
// json.Number it keeps a quoted string's quotes, so ParseDecimalRat rejects it
// (the schema's "number" type does not allow strings). Absent and null leave
// it empty.
type wireNum string

func (n *wireNum) UnmarshalJSON(b []byte) error {
	if string(b) != "null" {
		*n = wireNum(b)
	}
	return nil
}

// wireFinancials mirrors CommitmentFinancialsNoRebate. The nested schemas
// declare no required properties, so every number may be absent; an absent or
// null number decodes as an empty wireNum and becomes nil (unknown).
type wireFinancials struct {
	CommitmentCost struct {
		Total     wireNum `json:"total"`
		Breakdown struct {
			CloudProviderCost struct {
				Total wireNum `json:"total"`
			} `json:"cloud_provider_cost"`
			Premium wireNum `json:"archera_premium"`
		} `json:"breakdown"`
	} `json:"commitment_cost"`
	Savings struct {
		Net   wireNum `json:"net"`
		Gross wireNum `json:"gross"`
	} `json:"commitment_savings"`
	CoveredOnDemandCost wireNum `json:"covered_ondemand_cost"`
}

func (w *wireFinancials) toFinancials() (Financials, error) {
	var f Financials
	if w == nil {
		return f, fmt.Errorf("commitment_financials_monthly_rate: %w", errMissing)
	}
	var err error
	if f.CommitmentCostTotal, err = ratOrNil(w.CommitmentCost.Total); err != nil {
		return f, fmt.Errorf("commitment_cost.total: %w", err)
	}
	if f.CloudProviderCost, err = ratOrNil(w.CommitmentCost.Breakdown.CloudProviderCost.Total); err != nil {
		return f, fmt.Errorf("cloud_provider_cost.total: %w", err)
	}
	if f.Premium, err = ratOrNil(w.CommitmentCost.Breakdown.Premium); err != nil {
		return f, fmt.Errorf("archera_premium: %w", err)
	}
	if f.NetSavings, err = ratOrNil(w.Savings.Net); err != nil {
		return f, fmt.Errorf("commitment_savings.net: %w", err)
	}
	if f.GrossSavings, err = ratOrNil(w.Savings.Gross); err != nil {
		return f, fmt.Errorf("commitment_savings.gross: %w", err)
	}
	if f.CoveredOnDemandCost, err = ratOrNil(w.CoveredOnDemandCost); err != nil {
		return f, fmt.Errorf("covered_ondemand_cost: %w", err)
	}
	return f, nil
}

// wireTotals mirrors LineItemOfferComparisonTotals.
type wireTotals struct {
	Monthly     *wireFinancials `json:"commitment_financials_monthly_rate"`
	UpfrontCost wireNum         `json:"commitment_upfront_cost"`
}

func (w *wireTotals) toTotals() (Totals, error) {
	if w == nil {
		return Totals{}, errMissing
	}
	m, err := w.Monthly.toFinancials()
	if err != nil {
		return Totals{}, err
	}
	u, err := requiredRat(w.UpfrontCost)
	if err != nil {
		return Totals{}, fmt.Errorf("commitment_upfront_cost: %w", err)
	}
	return Totals{Monthly: m, UpfrontCost: u}, nil
}

type wireHypotheticalLineItem struct {
	LineItemID           string  `json:"line_item_id"`
	ActualTerm           *string `json:"actual_term"`
	ActualPaymentOption  *string `json:"actual_payment_option"`
	ActualCommitmentType *string `json:"actual_commitment_type"`
	ActualTermReason     string  `json:"actual_term_reason"`
}

func (w wireHypotheticalLineItem) toHypotheticalLineItem() (HypotheticalLineItem, error) {
	li := HypotheticalLineItem{LineItemID: w.LineItemID, ActualCommitmentType: w.ActualCommitmentType}
	if w.LineItemID == "" {
		return li, fmt.Errorf("line_item_id: %w", errMissing)
	}
	var err error
	if li.ActualTerm, err = nullableTerm(w.ActualTerm); err != nil {
		return li, fmt.Errorf("actual_term: %w", err)
	}
	if li.ActualPaymentOption, err = nullablePayment(w.ActualPaymentOption); err != nil {
		return li, fmt.Errorf("actual_payment_option: %w", err)
	}
	if li.Reason, err = parseTermReason(w.ActualTermReason); err != nil {
		return li, fmt.Errorf("actual_term_reason: %w", err)
	}
	return li, nil
}

// wireHypotheticalDelta mirrors HypotheticalDelta.
type wireHypotheticalDelta struct {
	MonthlyNetSavings     wireNum `json:"monthly_net_savings"`
	MonthlyCommitmentCost wireNum `json:"monthly_commitment_cost"`
	UpfrontCost           wireNum `json:"upfront_cost"`
}

// wireHypothetical mirrors HypotheticalTotal.
type wireHypothetical struct {
	ContractTerm  *string                     `json:"contract_term"`
	PaymentOption string                      `json:"payment_option"`
	Monthly       *wireFinancials             `json:"commitment_financials_monthly_rate"`
	UpfrontCost   wireNum                     `json:"commitment_upfront_cost"`
	Delta         *wireHypotheticalDelta      `json:"delta_vs_current"`
	LineItems     *[]wireHypotheticalLineItem `json:"line_items"`
}

func (w wireHypothetical) toHypothetical() (Hypothetical, error) {
	var h Hypothetical
	var err error
	if h.ContractTerm, err = nullableTerm(w.ContractTerm); err != nil {
		return h, fmt.Errorf("contract_term: %w", err)
	}
	if h.PaymentOption, err = parsePayment(w.PaymentOption); err != nil {
		return h, fmt.Errorf("payment_option: %w", err)
	}
	if h.Totals, err = (&wireTotals{Monthly: w.Monthly, UpfrontCost: w.UpfrontCost}).toTotals(); err != nil {
		return h, err
	}
	if w.Delta == nil {
		return h, fmt.Errorf("delta_vs_current: %w", errMissing)
	}
	if err := fillRats(
		numField{"delta_vs_current.monthly_net_savings", w.Delta.MonthlyNetSavings, &h.DeltaMonthlyNetSavings, true},
		numField{"delta_vs_current.monthly_commitment_cost", w.Delta.MonthlyCommitmentCost, &h.DeltaMonthlyCommitmentCost, true},
		numField{"delta_vs_current.upfront_cost", w.Delta.UpfrontCost, &h.DeltaUpfrontCost, true},
	); err != nil {
		return h, err
	}
	if w.LineItems == nil {
		return h, fmt.Errorf("line_items: %w", errMissing)
	}
	h.LineItems = make([]HypotheticalLineItem, 0, len(*w.LineItems))
	for i, wli := range *w.LineItems {
		li, err := wli.toHypotheticalLineItem()
		if err != nil {
			return h, fmt.Errorf("line_items[%d]: %w", i, err)
		}
		h.LineItems = append(h.LineItems, li)
	}
	return h, nil
}

// wireOfferEntry mirrors the consumed subset of OfferComparisonEntry.
// selected_amount and offer_org_id feed the vendor's update operation and are
// deliberately not decoded.
type wireOfferEntry struct {
	IsCurrent *bool  `json:"is_current"`
	OfferID   string `json:"offer_id"`
	Offer     *struct {
		Provider              *string `json:"provider"`
		Region                *string `json:"region"`
		GuaranteedDisplayName *string `json:"guaranteed_display_name"`
	} `json:"offer"`
	LeaseMenuItemID *string         `json:"lease_menu_item_id"`
	CommitmentType  string          `json:"commitment_type"`
	ContractTerm    *string         `json:"contract_term"`
	PaymentOption   *string         `json:"payment_option"`
	DiscountRate    wireNum         `json:"discount_rate"`
	BreakevenDays   wireNum         `json:"breakeven_days"`
	UpfrontCost     wireNum         `json:"commitment_upfront_cost"`
	Monthly         *wireFinancials `json:"commitment_financials_monthly_rate"`
	Delta           *wireOfferDelta `json:"delta_vs_current"`
}

// wireOfferDelta mirrors OfferComparisonDelta.
type wireOfferDelta struct {
	MonthlyNetSavings wireNum `json:"monthly_net_savings"`
	UpfrontCost       wireNum `json:"upfront_cost"`
	DiscountRate      wireNum `json:"discount_rate"`
	BreakevenDays     wireNum `json:"breakeven_days"`
}

// checkRequired rejects an entry missing a required, non-nullable field.
func (w *wireOfferEntry) checkRequired() error {
	if w == nil {
		return errMissing
	}
	switch {
	case w.IsCurrent == nil:
		return fmt.Errorf("is_current: %w", errMissing)
	case w.OfferID == "":
		return fmt.Errorf("offer_id: %w", errMissing)
	case w.Offer == nil:
		return fmt.Errorf("offer: %w", errMissing)
	case w.CommitmentType == "":
		return fmt.Errorf("commitment_type: %w", errMissing)
	case w.Delta == nil:
		return fmt.Errorf("delta_vs_current: %w", errMissing)
	}
	return nil
}

func (w *wireOfferEntry) toOfferEntry() (OfferEntry, error) {
	var e OfferEntry
	if err := w.checkRequired(); err != nil {
		return e, err
	}
	e.IsCurrent = *w.IsCurrent
	e.OfferID = w.OfferID
	e.CommitmentType = w.CommitmentType
	e.LeaseMenuItemID = w.LeaseMenuItemID
	e.GuaranteedDisplayName = w.Offer.GuaranteedDisplayName
	e.Region = w.Offer.Region
	var err error
	if e.Provider, err = parseProvider(w.Offer.Provider); err != nil {
		return e, fmt.Errorf("offer.provider: %w", err)
	}
	if e.ContractTerm, err = nullableTerm(w.ContractTerm); err != nil {
		return e, fmt.Errorf("contract_term: %w", err)
	}
	if e.PaymentOption, err = nullablePayment(w.PaymentOption); err != nil {
		return e, fmt.Errorf("payment_option: %w", err)
	}
	if e.Monthly, err = w.Monthly.toFinancials(); err != nil {
		return e, err
	}
	if err := fillRats(
		numField{"discount_rate", w.DiscountRate, &e.DiscountRate, true},
		numField{"breakeven_days", w.BreakevenDays, &e.BreakevenDays, false},
		numField{"commitment_upfront_cost", w.UpfrontCost, &e.UpfrontCost, true},
		numField{"delta_vs_current.monthly_net_savings", w.Delta.MonthlyNetSavings, &e.Delta.MonthlyNetSavings, true},
		numField{"delta_vs_current.upfront_cost", w.Delta.UpfrontCost, &e.Delta.UpfrontCost, true},
		numField{"delta_vs_current.discount_rate", w.Delta.DiscountRate, &e.Delta.DiscountRate, true},
		numField{"delta_vs_current.breakeven_days", w.Delta.BreakevenDays, &e.Delta.BreakevenDays, false},
	); err != nil {
		return e, err
	}
	return e, nil
}

// wireRow mirrors LineItemOfferComparisonRow.
type wireRow struct {
	LineItemID string            `json:"line_item_id"`
	Current    *wireOfferEntry   `json:"current"`
	Candidates *[]wireOfferEntry `json:"candidates"`
}

func (w wireRow) toRow() (ComparisonRow, error) {
	r := ComparisonRow{LineItemID: w.LineItemID}
	if w.LineItemID == "" {
		return r, fmt.Errorf("line_item_id: %w", errMissing)
	}
	var err error
	if r.Current, err = w.Current.toOfferEntry(); err != nil {
		return r, fmt.Errorf("current: %w", err)
	}
	if w.Candidates == nil {
		return r, fmt.Errorf("candidates: %w", errMissing)
	}
	r.Candidates = make([]OfferEntry, 0, len(*w.Candidates))
	for i := range *w.Candidates {
		c, err := (&(*w.Candidates)[i]).toOfferEntry()
		if err != nil {
			return r, fmt.Errorf("candidates[%d]: %w", i, err)
		}
		r.Candidates = append(r.Candidates, c)
	}
	return r, nil
}

// wireComparisonResponse mirrors LineItemOfferComparisonResponse.
type wireComparisonResponse struct {
	CurrentTotals      *wireTotals         `json:"current_totals"`
	HypotheticalTotals *[]wireHypothetical `json:"hypothetical_totals"`
	Data               *[]wireRow          `json:"data"`
}

// DecodeComparison parses exactly one LineItemOfferComparisonResponse JSON
// value and maps it into the shared Comparison type. orgID and planID come
// from the request, not the response body. fetchedAt is recorded verbatim;
// the API documents no TTL.
func DecodeComparison(r io.Reader, orgID, planID string, fetchedAt time.Time) (*Comparison, error) {
	var wire wireComparisonResponse
	if err := decodeOne(r, &wire); err != nil {
		return nil, fmt.Errorf("decoding comparison response: %w", err)
	}
	cur, err := wire.CurrentTotals.toTotals()
	if err != nil {
		return nil, fmt.Errorf("current_totals: %w", err)
	}
	if wire.HypotheticalTotals == nil {
		return nil, fmt.Errorf("hypothetical_totals: %w", errMissing)
	}
	if wire.Data == nil {
		return nil, fmt.Errorf("data: %w", errMissing)
	}
	c := &Comparison{
		OrgID:         orgID,
		PlanID:        planID,
		Current:       cur,
		Hypotheticals: make([]Hypothetical, 0, len(*wire.HypotheticalTotals)),
		Rows:          make([]ComparisonRow, 0, len(*wire.Data)),
		FetchedAt:     fetchedAt,
	}
	for i, wh := range *wire.HypotheticalTotals {
		h, err := wh.toHypothetical()
		if err != nil {
			return nil, fmt.Errorf("hypothetical_totals[%d]: %w", i, err)
		}
		c.Hypotheticals = append(c.Hypotheticals, h)
	}
	for i, wr := range *wire.Data {
		row, err := wr.toRow()
		if err != nil {
			return nil, fmt.Errorf("data[%d]: %w", i, err)
		}
		c.Rows = append(c.Rows, row)
	}
	return c, nil
}

// wirePlan mirrors the consumed subset of CommitmentPlan; every field here is
// required and non-nullable in the schema.
type wirePlan struct {
	ID                 string  `json:"id"`
	Status             string  `json:"status"`
	IsCalculating      *bool   `json:"is_calculating"`
	FeeHourly          wireNum `json:"fee_hourly"`
	SavingsHourly      wireNum `json:"savings_hourly"`
	CommitmentCoverage wireNum `json:"commitment_coverage"`
	MonthlySavings     wireNum `json:"monthly_savings"`
}

// DecodePlan parses exactly one CommitmentPlan JSON value.
func DecodePlan(r io.Reader) (*Plan, error) {
	var wire wirePlan
	if err := decodeOne(r, &wire); err != nil {
		return nil, fmt.Errorf("decoding plan response: %w", err)
	}
	if wire.ID == "" {
		return nil, fmt.Errorf("id: %w", errMissing)
	}
	if wire.IsCalculating == nil {
		return nil, fmt.Errorf("is_calculating: %w", errMissing)
	}
	status, err := parsePlanStatus(wire.Status)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	p := &Plan{ID: wire.ID, Status: status, IsCalculating: *wire.IsCalculating}
	for _, f := range []struct {
		name string
		in   wireNum
		out  **big.Rat
	}{
		{"fee_hourly", wire.FeeHourly, &p.FeeHourly},
		{"savings_hourly", wire.SavingsHourly, &p.SavingsHourly},
		{"commitment_coverage", wire.CommitmentCoverage, &p.CommitmentCoverage},
		{"monthly_savings", wire.MonthlySavings, &p.MonthlySavings},
	} {
		if *f.out, err = requiredRat(f.in); err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
	}
	return p, nil
}

// decodeOne decodes exactly one JSON value from r and rejects anything but
// whitespace after it.
func decodeOne(r io.Reader, v any) error {
	dec := json.NewDecoder(r)
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if errors.Is(err, errResponseTooLarge) {
			return err
		}
		return errors.New("unexpected data after JSON value")
	}
	return nil
}

// ratOrNil converts an optional or nullable JSON number to *big.Rat. Absent or
// null (empty wireNum) becomes nil: unknown, never zero. A malformed
// number is an error.
func ratOrNil(n wireNum) (*big.Rat, error) {
	s := string(n)
	if s == "" {
		return nil, nil
	}
	if err := checkNumberBounds(s); err != nil {
		return nil, err
	}
	return exchange.ParseDecimalRat(s)
}

// requiredRat converts a required, non-nullable JSON number; absent or null
// is a contract break.
func requiredRat(n wireNum) (*big.Rat, error) {
	r, err := ratOrNil(n)
	if err == nil && r == nil {
		return nil, errMissing
	}
	return r, err
}

func parsePayment(s string) (PaymentOption, error) {
	switch p := PaymentOption(s); p {
	case PaymentNoUpfront, PaymentPartialUpfront, PaymentAllUpfront:
		return p, nil
	case "":
		return "", errMissing
	default:
		return "", fmt.Errorf("unsupported value %q", s)
	}
}

func nullablePayment(s *string) (*PaymentOption, error) {
	if s == nil {
		return nil, nil
	}
	p, err := parsePayment(*s)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func parseTermReason(s string) (TermReason, error) {
	switch r := TermReason(s); r {
	case TermReasonExactMatch, TermReasonFallbackShorter, TermReasonNoAlternative, TermReasonBundleMember:
		return r, nil
	case "":
		return "", errMissing
	default:
		return "", fmt.Errorf("unsupported value %q", s)
	}
}

func parsePlanStatus(s string) (PlanStatus, error) {
	switch st := PlanStatus(s); st {
	case PlanStatusNew, PlanStatusReviewed, PlanStatusScheduled, PlanStatusCompleted,
		PlanStatusDraft, PlanStatusNeedsReview, PlanStatusInProgress, PlanStatusCancelled:
		return st, nil
	case "":
		return "", errMissing
	default:
		return "", fmt.Errorf("unsupported value %q", s)
	}
}

// parseProvider validates CommitmentOffer.provider. The offer schema declares
// no required properties, so an absent provider maps to "" (unknown).
func parseProvider(s *string) (common.ProviderType, error) {
	if s == nil {
		return "", nil
	}
	switch p := common.ProviderType(*s); p {
	case common.ProviderAWS, common.ProviderAzure, common.ProviderGCP:
		return p, nil
	default:
		return "", fmt.Errorf("unsupported value %q", *s)
	}
}

func nullableTerm(s *string) (*string, error) {
	if s == nil {
		return nil, nil
	}
	if _, ok := contractTerms[*s]; !ok {
		return nil, fmt.Errorf("unsupported value %q", *s)
	}
	return s, nil
}

// contractTerms is the documented contract_term enum, identical across
// OfferComparisonEntry.contract_term, HypotheticalTotal.contract_term and
// HypotheticalLineItem.actual_term.
var contractTerms = func() map[string]struct{} {
	m := map[string]struct{}{}
	for _, t := range []string{
		"one_year_gris", "thirty_day_gris", "two_month_gris",
		"three_month_gris", "four_month_gris", "five_month_gris",
		"six_month_gris", "seven_month_gris", "eight_month_gris",
		"nine_month_gris", "ten_month_gris", "eleven_month_gris",
		"twelve_month_gris", "thirteen_month_gris", "fourteen_month_gris",
		"fifteen_month_gris", "sixteen_month_gris", "seventeen_month_gris",
		"eighteen_month_gris", "nineteen_month_gris", "twenty_month_gris",
		"twenty_one_month_gris", "twenty_two_month_gris",
		"twenty_three_month_gris", "twenty_four_month_gris",
		"twenty_five_month_gris", "twenty_six_month_gris",
		"twenty_seven_month_gris", "twenty_eight_month_gris",
		"twenty_nine_month_gris", "thirty_month_gris", "thirty_one_month_gris",
		"thirty_two_month_gris", "thirty_three_month_gris",
		"thirty_four_month_gris", "thirty_five_month_gris", "one_year",
		"two_year", "three_year", "five_year", "zero_day", "thirty_day",
		"two_month", "three_month", "four_month", "five_month", "six_month",
		"seven_month", "eight_month", "nine_month", "ten_month",
		"eleven_month", "thirteen_month", "fourteen_month", "fifteen_month",
		"sixteen_month", "seventeen_month", "eighteen_month", "nineteen_month",
		"twenty_month", "twenty_one_month", "twenty_two_month",
		"twenty_three_month", "twenty_five_month", "twenty_six_month",
		"twenty_seven_month", "twenty_eight_month", "twenty_nine_month",
		"thirty_month", "thirty_one_month", "thirty_two_month",
		"thirty_three_month", "thirty_four_month", "thirty_five_month",
	} {
		m[t] = struct{}{}
	}
	return m
}()

// numField is one JSON number to convert into *big.Rat. A required field that
// is absent or null is an error; an optional one becomes nil (unknown).
type numField struct {
	name     string
	in       wireNum
	out      **big.Rat
	required bool
}

func fillRats(fields ...numField) error {
	for _, f := range fields {
		conv := ratOrNil
		if f.required {
			conv = requiredRat
		}
		r, err := conv(f.in)
		if err != nil {
			return fmt.Errorf("%s: %w", f.name, err)
		}
		*f.out = r
	}
	return nil
}

const (
	// maxNumberLen and maxExponent bound a number token before it reaches
	// big.Rat, which would otherwise materialize 1e999999 as a ~400 KB integer.
	// Money here is dollars and ratios; these limits are generous for that.
	maxNumberLen = 64
	maxExponent  = 100
)

// checkNumberBounds rejects over-long number tokens and extreme exponents.
func checkNumberBounds(s string) error {
	if len(s) > maxNumberLen {
		return fmt.Errorf("number token longer than %d bytes", maxNumberLen)
	}
	i := strings.IndexAny(s, "eE")
	if i < 0 {
		return nil
	}
	exp, err := strconv.Atoi(s[i+1:])
	if err != nil || exp > maxExponent || exp < -maxExponent {
		return fmt.Errorf("number exponent outside +/-%d", maxExponent)
	}
	return nil
}
