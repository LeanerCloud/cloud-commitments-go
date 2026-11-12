package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo

// SavingsPct returns the percentage a committed total saves against the
// on-demand total. It returns 0 when onDemandTotal is not positive, so a
// missing baseline never produces NaN or Inf.
func SavingsPct(onDemandTotal, committedTotal float64) float64 {
	if onDemandTotal <= 0 {
		return 0
	}
	return (onDemandTotal - committedTotal) / onDemandTotal * 100
}

// ShouldIncludeService reports whether service is selected by params: every
// service when params.Service is empty, otherwise only the requested one.
func ShouldIncludeService(params RecommendationParams, service ServiceType) bool {
	return params.Service == "" || params.Service == service
}
