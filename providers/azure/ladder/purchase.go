package ladder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/ladder"
)

const (
	minHourlyCommitment = 0.001
	maxHourlyCommitment = 1000000.0
	maxCommitmentPlaces = 5
)

// ErrLadderExecutionDisabled is returned by PurchaseLayer while the execution
// switch is off. It mirrors the AWS sentinel, which Azure cannot import.
var ErrLadderExecutionDisabled = errors.New("azure ladder execution disabled: purchase blocked by kill-switch")

// commitmentPurchaser is satisfied by the savings plan and compute reservation clients.
type commitmentPurchaser interface {
	PurchaseCommitment(ctx context.Context, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error)
}

// Option configures optional AzureLadder behavior at construction.
type Option func(*AzureLadder) error

// WithPurchase wires the write side. executionEnabled is the ladder execution
// switch; without this option the ladder refuses every purchase.
func WithPurchase(savingsPlans, reservations commitmentPurchaser, executionEnabled bool) Option {
	return func(a *AzureLadder) error {
		if isNil(savingsPlans) || isNil(reservations) {
			return errors.New("WithPurchase: purchase clients must not be nil")
		}
		a.spPurchase = savingsPlans
		a.riPurchase = reservations
		a.executionEnabled = executionEnabled
		return nil
	}
}

// isNil also catches a typed nil pointer held in an interface, which a plain
// == nil check misses and which would panic on the first call.
func isNil(c commitmentPurchaser) bool {
	if c == nil {
		return true
	}
	v := reflect.ValueOf(c)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// PurchaseLayer buys the layer's commitment through the existing Azure purchase
// client. Every check runs before the client call; with the switch off nothing
// is touched.
func (a *AzureLadder) PurchaseLayer(ctx context.Context, layer ladder.LayerType, rec common.Recommendation, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	if !a.executionEnabled {
		return common.PurchaseResult{}, fmt.Errorf("PurchaseLayer(%s): %w", layer, ErrLadderExecutionDisabled)
	}
	if rec.Provider != common.ProviderAzure {
		return common.PurchaseResult{}, fmt.Errorf("PurchaseLayer(%s): recommendation provider %q is not azure", layer, rec.Provider)
	}

	if opts.IdempotencyToken == "" {
		return common.PurchaseResult{}, fmt.Errorf("PurchaseLayer(%s): opts.IdempotencyToken must not be empty: a re-driven execution could double-buy", layer)
	}
	if opts.Source == "" {
		return common.PurchaseResult{}, fmt.Errorf("PurchaseLayer(%s): opts.Source must not be empty", layer)
	}

	client, err := a.prepareLayer(layer, &rec)
	if err != nil {
		return common.PurchaseResult{}, err
	}
	result, err := client.PurchaseCommitment(ctx, rec, opts)
	if err != nil {
		return result, fmt.Errorf("PurchaseLayer(%s): purchase failed: %w", layer, err)
	}
	return result, nil
}

// prepareLayer validates rec against the layer and returns the client that buys it.
func (a *AzureLadder) prepareLayer(layer ladder.LayerType, rec *common.Recommendation) (commitmentPurchaser, error) {
	switch layer {
	case ladder.LayerAzureSavingsPlan:
		if rec.Service != common.ServiceSavingsPlansAll && rec.Service != common.ServiceSavingsPlansCompute {
			return nil, fmt.Errorf("PurchaseLayer(%s): recommendation service %q is not an Azure savings plan service", layer, rec.Service)
		}
		if rec.CommitmentType != common.CommitmentSavingsPlan {
			return nil, fmt.Errorf("PurchaseLayer(%s): commitment type %q does not match the layer (want %s)", layer, rec.CommitmentType, common.CommitmentSavingsPlan)
		}
		if err := floorSavingsPlanAmount(rec); err != nil {
			return nil, fmt.Errorf("PurchaseLayer(%s): %w", layer, err)
		}
		return a.spPurchase, nil
	case ladder.LayerAzureReservation:
		if rec.Service != common.ServiceCompute {
			return nil, fmt.Errorf("PurchaseLayer(%s): recommendation service %q is not a VM reservation", layer, rec.Service)
		}
		if rec.CommitmentType != common.CommitmentReservedInstance {
			return nil, fmt.Errorf("PurchaseLayer(%s): commitment type %q does not match the layer (want %s)", layer, rec.CommitmentType, common.CommitmentReservedInstance)
		}
		if err := requireComputeDetails(rec.Details); err != nil {
			return nil, fmt.Errorf("PurchaseLayer(%s): %w", layer, err)
		}
		return a.riPurchase, nil
	default:
		return nil, fmt.Errorf("PurchaseLayer: layer %q is not a supported Azure ladder layer (want %s or %s)",
			layer, ladder.LayerAzureSavingsPlan, ladder.LayerAzureReservation)
	}
}

func requireComputeDetails(details common.ServiceDetails) error {
	switch d := details.(type) {
	case common.ComputeDetails:
		return nil
	case *common.ComputeDetails:
		if d == nil {
			return errors.New("recommendation Details must not be a nil *common.ComputeDetails")
		}
		return nil
	default:
		return fmt.Errorf("recommendation Details must be common.ComputeDetails, got %T", details)
	}
}

// floorSavingsPlanAmount range-checks the hourly commitment (go#354), then
// truncates it to maxCommitmentPlaces on a copy of the details so the caller's
// authorization data is untouched.
func floorSavingsPlanAmount(rec *common.Recommendation) error {
	details, ok := rec.Details.(*common.SavingsPlanDetails)
	if !ok || details == nil {
		return fmt.Errorf("recommendation Details must be *common.SavingsPlanDetails, got %T", rec.Details)
	}
	amount := details.HourlyCommitment
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return fmt.Errorf("SavingsPlanDetails.HourlyCommitment must be a positive finite value, got %g", amount)
	}
	if amount < minHourlyCommitment || amount > maxHourlyCommitment {
		return fmt.Errorf("SavingsPlanDetails.HourlyCommitment must be between %g and %g, got %g", minHourlyCommitment, maxHourlyCommitment, amount)
	}
	// Truncate the decimal text, not a scaled float that could cross a boundary.
	whole, fraction, _ := strings.Cut(strconv.FormatFloat(amount, 'f', -1, 64), ".")
	if len(fraction) > maxCommitmentPlaces {
		fraction = fraction[:maxCommitmentPlaces]
	}
	floored, err := strconv.ParseFloat(whole+"."+fraction+"0", 64)
	if err != nil {
		return fmt.Errorf("parse floored commitment: %w", err)
	}
	copyDetails := *details
	copyDetails.HourlyCommitment = floored
	rec.Details = &copyDetails
	return nil
}
