package spcommitment

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

const (
	minimumHourlyCommitment = 0.001
	maximumHourlyCommitment = 1000000.0
	maximumDecimalPlaces    = 5
)

// Normalize copies details so provider normalization cannot change caller authorization data.
func Normalize(rec *common.Recommendation) (string, error) {
	details, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok || details == nil {
		return "", fmt.Errorf("invalid service details for Savings Plans: recommendation Details must be *common.SavingsPlanDetails, got %T", rec.Details)
	}
	amount := details.HourlyCommitment
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return "", fmt.Errorf("SavingsPlanDetails.HourlyCommitment must be a positive finite value, got %g", amount)
	}
	if amount < minimumHourlyCommitment || amount > maximumHourlyCommitment {
		return "", fmt.Errorf("SavingsPlanDetails.HourlyCommitment must be between %g and %g, got %g", minimumHourlyCommitment, maximumHourlyCommitment, amount)
	}
	// Truncate decimal intent, rather than multiplying a binary float that can cross a boundary.
	wire := strconv.FormatFloat(amount, 'f', -1, 64)
	whole, fraction, _ := strings.Cut(wire, ".")
	if len(fraction) > maximumDecimalPlaces {
		fraction = fraction[:maximumDecimalPlaces]
	}
	fraction = strings.TrimRight(fraction, "0")
	fraction += strings.Repeat("0", max(0, 2-len(fraction)))
	wire = whole + "." + fraction
	normalized, err := strconv.ParseFloat(wire, 64)
	if err != nil {
		return "", fmt.Errorf("parse normalized Savings Plans commitment: %w", err)
	}
	copyDetails := *details
	copyDetails.HourlyCommitment = normalized
	rec.Details = &copyDetails
	return wire, nil
}
