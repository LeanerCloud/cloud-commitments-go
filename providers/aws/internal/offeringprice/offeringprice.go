// Package offeringprice turns the price fields of an AWS reserved-capacity
// offering into the figures common.OfferingDetails reports. All amounts are
// per ONE reservation in the offering's currency; callers multiply by the
// purchase count.
package offeringprice

import (
	"fmt"
	"math"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

const hourlyFrequency = "Hourly"

// Charge is one recurring charge of an offering.
type Charge struct {
	Amount    float64
	Frequency string
}

// Input is the price data of one offering.
type Input struct {
	// Service names the AWS service in error messages and term parsing.
	Service string
	// Term is the requested term (rec.Term); it must match the offering.
	Term string
	// FixedPrice is the upfront price per reservation.
	FixedPrice float64
	// UsagePrice is the legacy per-hour price per reservation. It is used
	// only when the recurring charges sum to zero.
	UsagePrice float64
	Charges    []Charge
	// DurationSeconds is the offering's own duration; 0 means missing.
	DurationSeconds int64
}

// Pricing is the priced offering, per one reservation.
type Pricing struct {
	// Upfront is the one-time price.
	Upfront float64
	// Hourly is the recurring price per hour.
	Hourly float64
	// Total is Upfront + Hourly * term hours, in the offering's currency.
	Total float64
	// EffectiveHourly is Total / term hours.
	EffectiveHourly float64
}

// Price computes the pricing of an offering. It fails on a missing or
// mismatched duration, a negative or non-finite amount, a recurring charge
// that is not hourly, and an offering with both a nonzero recurring charge
// and a nonzero UsagePrice (ambiguous, so not guessed).
func Price(in Input) (Pricing, error) {
	if in.DurationSeconds <= 0 {
		return Pricing{}, fmt.Errorf("%s offering has no duration; cannot price it", in.Service)
	}
	want, err := purchasecfg.DurationSeconds(in.Term, in.Service)
	if err != nil {
		return Pricing{}, err
	}
	if want != in.DurationSeconds {
		return Pricing{}, fmt.Errorf("%s offering duration %ds does not match requested term %q (%ds)", in.Service, in.DurationSeconds, in.Term, want)
	}
	if err := checkAmount("FixedPrice", in.FixedPrice); err != nil {
		return Pricing{}, err
	}
	hourly, err := hourlyRate(in)
	if err != nil {
		return Pricing{}, err
	}
	hours := float64(in.DurationSeconds) / 3600
	total := in.FixedPrice + hourly*hours
	return Pricing{Upfront: in.FixedPrice, Hourly: hourly, Total: total, EffectiveHourly: total / hours}, nil
}

func hourlyRate(in Input) (float64, error) {
	if err := checkAmount("UsagePrice", in.UsagePrice); err != nil {
		return 0, err
	}
	var sum float64
	for _, c := range in.Charges {
		if c.Frequency != hourlyFrequency {
			return 0, fmt.Errorf("%s offering has a %q recurring charge; only %q is supported", in.Service, c.Frequency, hourlyFrequency)
		}
		if err := checkAmount("RecurringChargeAmount", c.Amount); err != nil {
			return 0, err
		}
		sum += c.Amount
	}
	switch {
	case sum != 0 && in.UsagePrice != 0:
		return 0, fmt.Errorf("%s offering has both a recurring charge (%g) and a UsagePrice (%g); refusing to guess", in.Service, sum, in.UsagePrice)
	case sum != 0:
		return sum, nil
	default:
		return in.UsagePrice, nil
	}
}

func checkAmount(field string, v float64) error {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return fmt.Errorf("invalid %s %v", field, v)
	}
	return nil
}

// Currency returns the currency AWS reported, and fails when it is empty
// because a price without a currency cannot be compared to a USD ceiling.
func Currency(service, code string) (string, error) {
	if code == "" {
		return "", fmt.Errorf("%s offering reports no currency code", service)
	}
	return code, nil
}

// AssumedUSD is for offering types whose SDK struct has no currency field
// (ElastiCache, MemoryDB). Commercial and GovCloud AWS bill those in USD; the
// China partitions do not, so they fail instead of being labelled USD.
func AssumedUSD(service, region string) (string, error) {
	if strings.HasPrefix(region, "cn-") {
		return "", fmt.Errorf("%s offerings carry no currency code and region %q is not billed in USD", service, region)
	}
	return "USD", nil
}
