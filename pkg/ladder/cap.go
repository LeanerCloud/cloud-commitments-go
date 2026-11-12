package ladder

import (
	"fmt"
	"math"
)

// RequireCap validates a required money cap and returns it as a float64.
// nil is rejected: the caller must set an explicit cap, so an omitted field
// can never mean unlimited spend. Values must be finite and > 0 (a zero cap
// would block every exchange).
func RequireCap(name string, capUSD *float64) (float64, error) {
	if capUSD == nil {
		return 0, fmt.Errorf("%s is required: set an explicit cap (a nil cap is not treated as unlimited)", name)
	}
	v := *capUSD
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%s must be finite, got %g", name, v)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%s must be > 0 (a zero cap would block every exchange), got %g", name, v)
	}
	return v, nil
}
