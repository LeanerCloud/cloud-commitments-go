// Package scorer filters and ranks commitment purchase recommendations.
// It is a pure function package and must not import pkg/config.
package scorer

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Config controls which recommendations are allowed through the scorer.
// Zero values mean "no filter" for numeric thresholds, except that MinSavingsPctSet makes a zero MinSavingsPct a real floor; empty slice means "all services".
type Config struct {
	EnabledServices    []string // Empty = all services. E.g. ["ec2", "rds"].
	MinSavingsPct      float64  // Minimum savings percentage. 0 with MinSavingsPctSet false = no filter.
	MaxBreakEvenMonths int      // Maximum break-even months. 0 = no filter. No-upfront recs pass; recs whose break-even cannot be computed are filtered (an upfront payment option with no upfront price as FilterReasonPriceUnknown).
	MinCount           int      // Minimum count per recommendation. 0 = no filter. Does not apply to Savings Plans (dollar-denominated; their Count is always 1).
	MinSavingsPctSet   bool     // True makes MinSavingsPct a real floor even at 0 (drops recs with negative savings).
}

// Validate rejects a MinSavingsPct that is NaN, infinite or negative. Callers
// must run it before Score, which filters on whatever it is given.
func (c Config) Validate() error {
	if math.IsNaN(c.MinSavingsPct) || math.IsInf(c.MinSavingsPct, 0) {
		return fmt.Errorf("min_savings_pct must be a finite number")
	}
	if c.MinSavingsPct < 0 {
		return fmt.Errorf("min_savings_pct must be ≥ 0")
	}
	return nil
}

// FilterReasonPriceUnknown prefixes the FilterReason of a recommendation
// dropped by MaxBreakEvenMonths because its upfront price is unknown
// (common.UpfrontPriceUnknown).
const FilterReasonPriceUnknown = "price-unknown"

// FilteredRecommendation holds a recommendation that did not pass a filter, with the reason.
type FilteredRecommendation struct {
	FilterReason   string
	Recommendation common.Recommendation
}

// ScoredResult holds recommendations that passed the scorer (Passed) and those that did not (Filtered).
type ScoredResult struct {
	Passed   []common.Recommendation
	Filtered []FilteredRecommendation
}

// Score applies cfg filters to recs and returns passed and filtered recommendations.
// Passed recommendations are sorted by SavingsPercentage (desc), then EstimatedSavings (desc),
// then a stable tie-breaker of Service+Region+ResourceType (asc) for deterministic output.
func Score(recs []common.Recommendation, cfg Config) ScoredResult {
	result := ScoredResult{
		Passed:   make([]common.Recommendation, 0, len(recs)),
		Filtered: make([]FilteredRecommendation, 0),
	}

	enabledSet := buildServiceSet(cfg.EnabledServices)

	for i := range recs {
		rec := &recs[i]
		if reason := filterReason(*rec, cfg, enabledSet); reason != "" {
			result.Filtered = append(result.Filtered, FilteredRecommendation{
				Recommendation: *rec,
				FilterReason:   reason,
			})
		} else {
			passed := *rec
			// Surface the computed value so reports show it; never write +Inf
			// (not JSON-encodable) or the 0 of "no upfront / unknown".
			if months, known := common.BreakEven(passed); known && months > 0 && !math.IsInf(months, 0) {
				passed.BreakEvenMonths = months
			}
			result.Passed = append(result.Passed, passed)
		}
	}

	sort.Slice(result.Passed, func(i, j int) bool {
		a, b := result.Passed[i], result.Passed[j]
		if a.SavingsPercentage != b.SavingsPercentage {
			return a.SavingsPercentage > b.SavingsPercentage
		}
		if a.EstimatedSavings != b.EstimatedSavings {
			return a.EstimatedSavings > b.EstimatedSavings
		}
		keyA := string(a.Service) + "|" + a.Region + "|" + a.ResourceType
		keyB := string(b.Service) + "|" + b.Region + "|" + b.ResourceType
		return keyA < keyB
	})

	return result
}

// filterReason returns a non-empty string describing why rec was filtered, or "" if it passes.
func filterReason(rec common.Recommendation, cfg Config, enabledServices map[string]struct{}) string {
	if len(enabledServices) > 0 {
		if _, ok := enabledServices[strings.ToLower(string(rec.Service))]; !ok {
			return fmt.Sprintf("service %q not in enabled list", rec.Service)
		}
	}

	if (cfg.MinSavingsPct > 0 || cfg.MinSavingsPctSet) && !(rec.SavingsPercentage >= cfg.MinSavingsPct) { // negated so a NaN floor fails closed
		return fmt.Sprintf("savings %.1f%% below minimum %.1f%%", rec.SavingsPercentage, cfg.MinSavingsPct)
	}

	if cfg.MaxBreakEvenMonths > 0 {
		if reason := breakEvenReason(rec, cfg.MaxBreakEvenMonths); reason != "" {
			return reason
		}
	}

	if reason := minCountReason(rec, cfg.MinCount); reason != "" {
		return reason
	}

	return ""
}

// minCountReason returns why rec fails a positive minCount, or "" if it passes.
// Savings Plans are exempt: they are dollar-denominated and always carry Count 1.
func minCountReason(rec common.Recommendation, minCount int) string {
	if minCount > 0 && rec.CommitmentType != common.CommitmentSavingsPlan && rec.Count < minCount {
		return fmt.Sprintf("count %d below minimum %d", rec.Count, minCount)
	}
	return ""
}

// breakEvenReason returns why rec fails a positive maxMonths break-even
// filter, or "" if it passes. Unknown break-even fails closed.
func breakEvenReason(rec common.Recommendation, maxMonths int) string {
	months, known := common.BreakEven(rec)
	if !known {
		if common.UpfrontPriceUnknown(rec) {
			return fmt.Sprintf("%s: no upfront price for payment option %q; --max-break-even-months %d cannot be enforced", FilterReasonPriceUnknown, rec.PaymentOption, maxMonths)
		}
		return fmt.Sprintf("break-even cannot be computed (upfront %.2f, savings %.2f, term %q); --max-break-even-months %d cannot be enforced", rec.CommitmentCost, rec.EstimatedSavings, rec.Term, maxMonths)
	}
	if months > float64(maxMonths) {
		return fmt.Sprintf("break-even %.1f months exceeds maximum %d months", months, maxMonths)
	}
	return ""
}

// buildServiceSet converts a slice of service names to a lowercase set for O(1) lookup.
func buildServiceSet(services []string) map[string]struct{} {
	if len(services) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(services))
	for _, s := range services {
		set[strings.ToLower(s)] = struct{}{}
	}
	return set
}
