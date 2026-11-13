package common //nolint:revive // pkg/common is the module's core domain package (Recommendation, ProviderType, ...), imported by name (common.X) throughout this repo and by consumer-mcp; renaming would break every consumer import

import (
	"math"
	"strings"
)

// BreakEven returns the number of months until the upfront payment of rec is
// recovered by its recurring discount, and whether that is known.
//
//   - A positive rec.BreakEvenMonths set by a provider wins.
//   - No upfront (CommitmentCost == 0): (0, true). There is nothing to recover.
//     Exception: a zero upfront on a payment option that carries an upfront
//     (see UpfrontPriceUnknown) is a missing price, not a known zero: (0, false).
//   - Otherwise upfront / gross monthly savings, where gross monthly savings is
//     EstimatedSavings plus the upfront amortized over the term. EstimatedSavings
//     is the net monthly figure (already after the amortized upfront), so using
//     it alone would overstate the break-even and push all-upfront commitments
//     past their own term.
//   - Net savings so low that gross monthly savings is not positive: (+Inf, true).
//     The commitment never pays back its upfront.
//   - Negative or non-finite money fields, or a term that is not 1yr/3yr:
//     (0, false). Unknown is not 0 and is never reported as a break-even.
//
// The result is invariant under ScaleRecommendationCosts: upfront and savings
// scale together.
func BreakEven(rec Recommendation) (months float64, known bool) {
	if rec.BreakEvenMonths > 0 && !math.IsInf(rec.BreakEvenMonths, 0) && !math.IsNaN(rec.BreakEvenMonths) {
		return rec.BreakEvenMonths, true
	}
	upfront, savings := rec.CommitmentCost, rec.EstimatedSavings
	if !isFinite(upfront) || !isFinite(savings) || upfront < 0 {
		return 0, false
	}
	if upfront == 0 {
		return 0, !UpfrontPriceUnknown(rec)
	}
	termMonths, ok := breakEvenTermMonths(string(rec.Term))
	if !ok {
		return 0, false
	}
	gross := savings + upfront/termMonths
	if gross <= 0 {
		return math.Inf(1), true
	}
	return upfront / gross, true
}

func breakEvenTermMonths(term string) (float64, bool) {
	switch normalizeReservationTerm(term) {
	case string(Term1Year):
		return 12, true
	case string(Term3Year):
		return 36, true
	default:
		return 0, false
	}
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// paymentKeyReplacer folds payment spellings ("all-upfront", "All_Upfront")
// to one key. The typed constants are folded with it too, so their case arms match.
var paymentKeyReplacer = strings.NewReplacer("-", "", "_", "", " ", "")

func paymentKey(p PaymentOption) string { return paymentKeyReplacer.Replace(string(p)) }

// UpfrontPriceUnknown reports whether rec's zero CommitmentCost is a missing
// price rather than a known zero upfront: the payment option says an upfront
// is charged (all, partial or Azure "upfront"), but the provider gave no
// figure. Azure Advisor "upfront" variants and AWS recommendations with a nil
// UpfrontCost have this shape. No-upfront, monthly and empty or unrecognized
// payment options keep a zero as a known zero upfront.
func UpfrontPriceUnknown(rec Recommendation) bool {
	if rec.CommitmentCost != 0 {
		return false
	}
	switch paymentKeyReplacer.Replace(strings.ToLower(strings.TrimSpace(string(rec.PaymentOption)))) {
	case paymentKey(PaymentAllUpfront), "upfront", paymentKey(PaymentPartialUpfront), "partial":
		return true
	default:
		return false
	}
}
