package skumatch

// SavingsPercent returns the percentage a commitment saves against on-demand
// over a term. commitmentTotal must already be a term total (per-hour price *
// hoursInTerm), on the same basis as onDemandPrice * hoursInTerm. A zero
// on-demand total yields NaN or Inf, so callers must reject a zero on-demand
// price first.
func SavingsPercent(onDemandPrice, hoursInTerm, commitmentTotal float64) float64 {
	onDemandTotal := onDemandPrice * hoursInTerm
	return ((onDemandTotal - commitmentTotal) / onDemandTotal) * 100
}
