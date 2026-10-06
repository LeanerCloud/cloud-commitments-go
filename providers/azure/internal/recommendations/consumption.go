package recommendations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/consumption/armconsumption"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/logging"
	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/pricing"
)

// DaysPerMonth is the average month length (365.25 / 12) used to convert
// Azure's lookback-window amounts into monthly run-rates.
const DaysPerMonth = 30.4375

// consumptionLookBack is the lookback window requested from the Consumption
// API. It is sent explicitly in every filter so the scaling multiplier never
// depends on an API-side default.
const consumptionLookBack = "Last7Days"

// currencyUSD is the only currency the Retail Prices lookups are made in, so
// it is the only one a recommendation may be denominated in.
const currencyUSD = "USD"

// ConsumptionFilter builds the OData filter for the Consumption
// ReservationRecommendations API: Shared scope, one resource type and an
// explicit lookback window.
func ConsumptionFilter(resourceType string) string {
	return "properties/scope eq 'Shared' and properties/resourceType eq '" + resourceType +
		"' and properties/lookBackPeriod eq '" + consumptionLookBack + "'"
}

// ExtractConsumption is Extract for the Consumption API path. The Consumption
// API returns CostWithNoReservedInstances, TotalCostWithReservedInstances and
// NetSavings as amounts over the lookback window (not monthly figures and not
// term totals), so the result is rescaled to monthly run-rates using the
// lookback carried on the response:
//
//	OnDemandCost     = rawOnDemand x DaysPerMonth / lookBackDays
//	EstimatedSavings = (rawOnDemand - rawTotalWithRI) x DaysPerMonth / lookBackDays
//
// Commitment is list price (see ExpandConsumptionVariants) while savings is
// net of on-demand overage, so they are not expected to reconcile exactly.
//
// CommitmentCost is left 0 and RecurringMonthlyCost nil: the commitment comes
// from the reservation retail price, not from the window amounts.
//
// It returns (nil, nil) for the payloads Extract refuses, and an error for a
// recommendation that cannot be scaled safely: missing or unknown lookback,
// missing or unknown term, missing cost fields, or a non-USD / unlabelled
// modern currency. Callers skip the recommendation on error.
func ExtractConsumption(rec armconsumption.ReservationRecommendationClassification) (*ExtractedFields, error) {
	f := Extract(rec)
	if f == nil {
		return nil, nil
	}
	meta, err := consumptionMeta(rec)
	if err != nil {
		return nil, fmt.Errorf("recommendation for %q in %q: %w", f.ResourceType, f.Region, err)
	}
	if _, err := TermYears(f.Term); err != nil {
		return nil, fmt.Errorf("recommendation for %q in %q: %w", f.ResourceType, f.Region, err)
	}
	if !meta.termPresent {
		return nil, fmt.Errorf("recommendation for %q in %q: missing term", f.ResourceType, f.Region)
	}

	scale := DaysPerMonth / float64(meta.lookBackDays)
	rawOnDemand, rawTotal := f.OnDemandCost, f.CommitmentCost
	f.OnDemandCost = rawOnDemand * scale
	f.EstimatedSavings = (rawOnDemand - rawTotal) * scale
	f.CommitmentCost = 0
	f.RecurringMonthlyCost = nil
	f.LookBackDays = meta.lookBackDays
	return f, nil
}

type consumptionMetadata struct {
	lookBackDays int
	termPresent  bool
}

func consumptionMeta(rec armconsumption.ReservationRecommendationClassification) (consumptionMetadata, error) {
	switch v := rec.(type) {
	case *armconsumption.LegacyReservationRecommendation:
		return legacyMeta(v.Properties.GetLegacyReservationRecommendationProperties())
	case *armconsumption.ModernReservationRecommendation:
		return modernMeta(v.Properties)
	default:
		return consumptionMetadata{}, fmt.Errorf("unsupported recommendation type %T", rec)
	}
}

func legacyMeta(props *armconsumption.LegacyReservationRecommendationProperties) (consumptionMetadata, error) {
	if props.CostWithNoReservedInstances == nil || props.TotalCostWithReservedInstances == nil {
		return consumptionMetadata{}, fmt.Errorf("missing CostWithNoReservedInstances or TotalCostWithReservedInstances")
	}
	days, err := parseLegacyLookBack(props.LookBackPeriod)
	return consumptionMetadata{lookBackDays: days, termPresent: props.Term != nil && *props.Term != ""}, err
}

func modernMeta(props *armconsumption.ModernReservationRecommendationProperties) (consumptionMetadata, error) {
	if err := requireUSDAmount("CostWithNoReservedInstances", props.CostWithNoReservedInstances); err != nil {
		return consumptionMetadata{}, err
	}
	if err := requireUSDAmount("TotalCostWithReservedInstances", props.TotalCostWithReservedInstances); err != nil {
		return consumptionMetadata{}, err
	}
	days, err := checkLookBackDays(props.LookBackPeriod)
	return consumptionMetadata{lookBackDays: days, termPresent: props.Term != nil && *props.Term != ""}, err
}

func requireUSDAmount(name string, a *armconsumption.Amount) error {
	if a == nil || a.Value == nil {
		return fmt.Errorf("missing %s", name)
	}
	if a.Currency == nil || !strings.EqualFold(*a.Currency, currencyUSD) {
		return fmt.Errorf("%s currency %q is not %s", name, strDeref(a.Currency), currencyUSD)
	}
	return nil
}

// parseLegacyLookBack maps the legacy "Last7Days"/"Last30Days"/"Last60Days"
// enum to a day count.
func parseLegacyLookBack(s *string) (int, error) {
	if s == nil || *s == "" {
		return 0, fmt.Errorf("missing lookBackPeriod")
	}
	switch *s {
	case string(armconsumption.LookBackPeriodLast07Days):
		return 7, nil
	case string(armconsumption.LookBackPeriodLast30Days):
		return 30, nil
	case string(armconsumption.LookBackPeriodLast60Days):
		return 60, nil
	default:
		return 0, fmt.Errorf("unknown lookBackPeriod %q", *s)
	}
}

// checkLookBackDays validates the modern integer lookback, which Azure
// documents as 7, 30 or 60.
func checkLookBackDays(d *int32) (int, error) {
	if d == nil {
		return 0, fmt.Errorf("missing lookBackPeriod")
	}
	switch *d {
	case 7, 30, 60:
		return int(*d), nil
	default:
		return 0, fmt.Errorf("unknown lookBackPeriod %d", *d)
	}
}

// TermYears maps a normalised term ("1yr", "3yr") to years and fails on
// anything else.
func TermYears(term string) (int, error) {
	switch term {
	case "1yr":
		return 1, nil
	case "3yr":
		return 3, nil
	default:
		return 0, fmt.Errorf("unrecognized term %q", term)
	}
}

// ReservationPrice is the retail price of one reservation unit for a whole
// term (the Retail Prices API's priceType 'Reservation' row), in Currency.
type ReservationPrice struct {
	Total    float64
	Currency string
}

// PriceFunc resolves the term-total retail price of one reservation unit.
type PriceFunc func(ctx context.Context, resourceType, region string, termYears int) (ReservationPrice, error)

type priceKey struct {
	resourceType, region string
	termYears            int
}

type priceResult struct {
	price float64
	err   error
}

// Pricer resolves reservation unit prices through lookup, once per distinct
// (service, SKU, region, term). A SKU with no usable retail row is cached, so
// it costs one HTTP lookup, not one per recommendation; a failure to reach the
// catalog (see IsCollectionFailure) is never cached. It is meant
// to live for one GetRecommendations call and is not safe for concurrent use.
type Pricer struct {
	service string
	lookup  PriceFunc
	cache   map[priceKey]priceResult
}

// NewPricer returns a Pricer for service backed by lookup.
func NewPricer(service string, lookup PriceFunc) *Pricer {
	return &Pricer{service: service, lookup: lookup, cache: make(map[priceKey]priceResult)}
}

// UnitPrice returns the USD term-total price of one reservation unit.
func (p *Pricer) UnitPrice(ctx context.Context, resourceType, region string, termYears int) (float64, error) {
	key := priceKey{resourceType, region, termYears}
	if r, ok := p.cache[key]; ok {
		return r.price, r.err
	}
	r := p.resolve(ctx, key)
	if !IsCollectionFailure(r.err) {
		p.cache[key] = r
	}
	return r.price, r.err
}

// IsCollectionFailure reports whether err means the price lookup itself
// failed (transport error, HTTP error status, canceled or expired context),
// so the whole collection is unreliable, rather than the catalog having no
// usable price for one recommendation.
func IsCollectionFailure(err error) bool {
	return errors.Is(err, pricing.ErrFetch) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (p *Pricer) resolve(ctx context.Context, key priceKey) priceResult {
	rp, err := p.lookup(ctx, key.resourceType, key.region, key.termYears)
	if err != nil {
		return priceResult{err: fmt.Errorf("%s reservation price for %q in %q (%dy): %w", p.service, key.resourceType, key.region, key.termYears, err)}
	}
	if !strings.EqualFold(rp.Currency, currencyUSD) {
		return priceResult{err: fmt.Errorf("%s reservation price for %q in %q (%dy) is in %q, not %s", p.service, key.resourceType, key.region, key.termYears, rp.Currency, currencyUSD)}
	}
	if rp.Total <= 0 {
		return priceResult{err: fmt.Errorf("%s reservation price for %q in %q (%dy) is not positive: %v", p.service, key.resourceType, key.region, key.termYears, rp.Total)}
	}
	return priceResult{price: rp.Total}
}

// ExpandConsumptionVariants fans a Consumption-path recommendation (from
// ExtractConsumption, monthly OnDemandCost and EstimatedSavings already set)
// out into its two payment variants, with the commitment taken from the
// reservation retail price: Count x the term-total unit price.
//
//   - "upfront": CommitmentCost = Count x price, RecurringMonthlyCost = 0.
//   - "monthly": CommitmentCost = 0, RecurringMonthlyCost = Count x price / term months.
//
// Azure charges the same total for both billing plans. An error means the
// recommendation cannot be priced and must be skipped.
func ExpandConsumptionVariants(ctx context.Context, base common.Recommendation, p *Pricer) ([]common.Recommendation, error) {
	years, err := TermYears(base.Term)
	if err != nil {
		return nil, err
	}
	if base.Count <= 0 {
		return nil, fmt.Errorf("recommendation for %q in %q has non-positive count %d", base.ResourceType, base.Region, base.Count)
	}
	unit, err := p.UnitPrice(ctx, base.ResourceType, base.Region, years)
	if err != nil {
		return nil, err
	}
	total := float64(base.Count) * unit

	var savingsPct float64
	if base.OnDemandCost != 0 {
		savingsPct = base.EstimatedSavings / base.OnDemandCost * 100
	}

	upfront := base
	upfront.PaymentOption = "upfront"
	upfront.CommitmentCost = total
	upfront.RecurringMonthlyCost = float64Ptr(0)
	upfront.SavingsPercentage = savingsPct

	monthly := base
	monthly.PaymentOption = "monthly"
	monthly.CommitmentCost = 0
	monthly.RecurringMonthlyCost = float64Ptr(total / float64(years*12))
	monthly.SavingsPercentage = savingsPct

	return []common.Recommendation{upfront, monthly}, nil
}

// ExtractConsumptionOrSkip wraps ExtractConsumption for the service
// converters: a recommendation that cannot be scaled is logged as an error
// and reported as nil, so one bad recommendation does not fail the collection.
func ExtractConsumptionOrSkip(service string, rec armconsumption.ReservationRecommendationClassification) *ExtractedFields {
	f, err := ExtractConsumption(rec)
	if err != nil {
		logging.Errorf("azure %s: skipping recommendation: %v", service, err)
		return nil
	}
	return f
}

// AppendConsumptionVariants appends the variants of base to recs. A
// recommendation the catalog has no usable price for is logged as an error and
// skipped, leaving recs unchanged. A price lookup that fails to complete
// returns the error (see IsCollectionFailure): the caller must fail the
// collection rather than return a silently partial result.
func AppendConsumptionVariants(ctx context.Context, service string, recs []common.Recommendation, base common.Recommendation, p *Pricer) ([]common.Recommendation, error) {
	variants, err := ExpandConsumptionVariants(ctx, base, p)
	if err != nil {
		if IsCollectionFailure(err) {
			return recs, fmt.Errorf("azure %s: %w", service, err)
		}
		logging.Errorf("azure %s: skipping recommendation: %v", service, err)
		return recs, nil
	}
	return append(recs, variants...), nil
}
