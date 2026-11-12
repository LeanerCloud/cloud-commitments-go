package ladder

import (
	"fmt"
	"math"
	"slices"
	"sort"
)

const (
	// minStableSeriesDays is two full weekly cycles, so weekday/weekend
	// seasonality is visible before a base layer is sized from the series.
	minStableSeriesDays = 14
	// stableFloorPercentile is independent of the configured low-water
	// percentile, which can be up to 100 (the series maximum).
	stableFloorPercentile = 10
	// minFloorToMedianRatio gates volatile series: the floor must be at least
	// this share of typical usage. 0.5 is exact in binary, so boundaries are exact.
	minFloorToMedianRatio = 0.5
	// trailingMinDays catches a sudden drop that the percentile terms miss.
	trailingMinDays = 3
)

// finiteNonNeg reports whether v is finite and non-negative; NaN fails >=.
func finiteNonNeg(v float64) bool { return v >= 0 && !math.IsInf(v, 0) }

// unitFraction reports whether f is in [0, 1); NaN fails the comparisons.
func unitFraction(f float64) bool { return f >= 0 && f < 1 }

// validStableInputs guarantees a non-empty series of finite non-negative
// values, a finite non-negative lowWater and f in [0, 1).
func validStableInputs(series []float64, lowWater, f float64) bool {
	if len(series) == 0 {
		return false
	}
	for _, v := range series {
		if !finiteNonNeg(v) {
			return false
		}
	}
	return finiteNonNeg(lowWater) && unitFraction(f)
}

// stableQuantiles returns the floor percentile and the nearest-rank median
// (the lower median for even N).
func stableQuantiles(series []float64) (p10, median float64, err error) {
	p10, err = nearestRankPercentile(series, stableFloorPercentile)
	if err != nil {
		return 0, 0, err
	}
	median, err = nearestRankPercentile(series, 50)
	if err != nil {
		return 0, 0, err
	}
	return p10, median, nil
}

// stableGatePasses is true for a long enough, steady, non-zero series. The
// ratio is compared by multiplication, and median > 0 stops an all-zero series
// passing 0 >= 0.5*0.
func stableGatePasses(n int, p10, median float64) bool {
	return n >= minStableSeriesDays && median > 0 && p10 >= minFloorToMedianRatio*median
}

// stableBase is the lowest of the low-water mark, the floor percentile and the
// trailing minimum, so every term can only lower the estimate. PRECONDITION:
// len(series) >= trailingMinDays; the minStableSeriesDays gate must run first.
func stableBase(series []float64, lowWater, p10 float64) float64 {
	return min(lowWater, p10, slices.Min(series[len(series)-trailingMinDays:]))
}

// estimateStableUSDPerHour returns the post-buffer stable usage, or nil when
// the series cannot support one (invalid input, short, volatile, or zero).
// The error is reserved for a percentile failure, which validStableInputs makes
// unreachable.
func estimateStableUSDPerHour(series []float64, lowWater, bufferFraction float64) (*float64, error) {
	if !validStableInputs(series, lowWater, bufferFraction) {
		return nil, nil
	}
	p10, median, err := stableQuantiles(series)
	if err != nil {
		return nil, err
	}
	if !stableGatePasses(len(series), p10, median) {
		return nil, nil
	}
	v := stableBase(series, lowWater, p10) * (1 - bufferFraction)
	if v <= 0 || math.IsInf(v, 0) || math.IsNaN(v) {
		return nil, nil
	}
	return ptr(v), nil
}

// familyStableUSDPerHour sizes the base layer per instance family. With two or
// more families it returns min(flat, sum over families of minimum observed
// usage * (1 - bufferFraction)): a family-bound commitment is sized from that
// family's own floor, and the sum of minima never exceeds the flat estimate.
// With zero or one family the family series is the flat series (or absent), so
// the flat estimate stands unchanged. A nil flat estimate stays nil. Family
// series are validated (fail loud) only on the two-or-more path; a family fetch
// error is handled by the caller, not here.
func familyStableUSDPerHour(families map[string][]float64, flat *float64, bufferFraction float64) (*float64, error) {
	if len(families) < 2 {
		return flat, nil
	}
	names := make([]string, 0, len(families))
	for name := range families {
		names = append(names, name)
	}
	sort.Strings(names)
	total := 0.0
	for _, name := range names {
		series := families[name]
		if len(series) == 0 {
			return nil, fmt.Errorf("family %s: series is empty", name)
		}
		if err := validateSeries(series); err != nil {
			return nil, fmt.Errorf("family %s: %w", name, err)
		}
		total += slices.Min(series) * (1 - bufferFraction)
	}
	if flat == nil || total <= 0 {
		return nil, nil
	}
	return ptr(min(total, *flat)), nil
}
