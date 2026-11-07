package redshift

import (
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

// fit is how one reserved node relates to the purchase being reconciled.
type fit int

const (
	fitNo        fit = iota // a field is present and different: not this purchase
	fitUncertain            // cannot be ruled in or out
	fitMatch                // every required field is present and equal
)

// secondsPerMonth is the arithmetic matchesDuration uses for offerings.
const secondsPerMonth = 2592000

// presentFit: nil is doubt, an equal value matches, a different one rules the node out.
func presentFit[T comparable](got *T, want T) fit {
	switch {
	case got == nil:
		return fitUncertain
	case *got == want:
		return fitMatch
	}
	return fitNo
}

// nodeFit classifies one reserved node against the purchase, section 4.2 of the plan. A definite
// mismatch beats doubt, doubt beats a match; only a live node with every field present and equal
// is fitMatch.
func nodeFit(node *redshifttypes.ReservedNode, offeringID string, count int, nodeType string, months int, lo, hi time.Time) fit {
	fits := []fit{
		presentFit(node.ReservedNodeOfferingId, offeringID),
		presentFit(node.NodeType, nodeType),
		nodeCountFit(node.NodeCount, count),
		durationFit(node.Duration, months),
		startFit(node.StartTime, lo, hi),
		stateFit(node.State),
	}
	if aws.ToString(node.ReservedNodeId) == "" {
		fits = append(fits, fitUncertain)
	}
	f := fitMatch
	for _, x := range fits {
		f = min(f, x)
	}
	return f
}

func nodeCountFit(got *int32, want int) fit {
	if got == nil {
		return fitUncertain
	}
	return presentFit(aws.Int(int(*got)), want)
}

func durationFit(got *int32, months int) fit {
	if got == nil {
		return fitUncertain
	}
	return presentFit(aws.Int(int(*got/secondsPerMonth)), months)
}

func startFit(got *time.Time, lo, hi time.Time) fit {
	switch {
	case got == nil:
		return fitUncertain
	case purchasecfg.InWindow(*got, lo, hi):
		return fitMatch
	}
	return fitNo
}

// stateFit: only live states can be reported Found; any other state (retired, payment-failed,
// exchanging, empty) with matching attributes is doubt. The SDK defines no enum for the field.
func stateFit(state *string) fit {
	switch aws.ToString(state) {
	case "active", redshiftStatePendingPayment, "payment-pending":
		return fitMatch
	}
	return fitUncertain
}
