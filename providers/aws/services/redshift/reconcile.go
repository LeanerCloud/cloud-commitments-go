package redshift

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

var reconcileCriteria = []string{"offering-id", "count", "node-type", "duration", "start-window"}

// reconcileParams validates the request; reason is non-empty on refusal.
func (c *Client) reconcileParams(req common.ReconcileRequest) (count int32, months int, lo, hi time.Time, reason string) {
	lo, hi, ok := purchasecfg.ReconcileWindow(req.Options.RequestedAt, req.AttemptEndedAt)
	if !ok {
		return 0, 0, lo, hi, purchasecfg.ReasonInvalidWindow
	}
	rec := req.Recommendation
	count, countErr := offeringprice.PurchaseCount(rec.Count)
	months, termErr := requiredMonthsForTerm(rec.Term)
	if rec.Region != c.region || countErr != nil || termErr != nil ||
		rec.ResourceType == "" || !supportedPaymentOption(rec.PaymentOption) {
		return 0, 0, lo, hi, purchasecfg.ReasonInvalidRequest
	}
	return count, months, lo, hi, ""
}

// scanFailure maps an incomplete describeAllNodes scan to its Reason.
func scanFailure(err error) string {
	if errors.Is(err, errScanCapReached) {
		return purchasecfg.ReasonLookupIncomplete
	}
	return purchasecfg.ReasonLookupFailed
}

// ReconcilePurchase reports, read-only, whether a purchase that returned ErrOutcomeUnknown landed.
// Any doubt or failure is Unknown; see purchasecfg.Decide.
func (c *Client) ReconcilePurchase(ctx context.Context, req common.ReconcileRequest) common.ReconcileResult {
	count, months, lo, hi, reason := c.reconcileParams(req)
	if reason != "" {
		return purchasecfg.UnknownResult(reason)
	}
	if ctx.Err() != nil {
		return purchasecfg.UnknownResult(purchasecfg.ReasonLookupFailed)
	}

	rec := req.Recommendation
	offeringID, err := c.findOfferingID(ctx, rec, req.Options.ExecutionID)
	if err != nil || offeringID == "" {
		return purchasecfg.UnknownResult(purchasecfg.ReasonOfferingUnresolved)
	}
	nodes, err := c.describeAllNodes(ctx)
	if err != nil {
		return purchasecfg.UnknownResult(scanFailure(err))
	}

	matches, uncertain := classifyNodes(nodes, offeringID, int(count), rec.ResourceType, months, lo, hi)
	matches, uncertain, ok := c.reconcileClaims(ctx, req, matches, uncertain)
	if !ok {
		return purchasecfg.UnknownResult(purchasecfg.ReasonClaimLookupFailed)
	}
	// Tagged is EC2-only: Redshift reserved nodes cannot carry the idempotency tag, so it is always false.
	return purchasecfg.Decide(matches, uncertain, reconcileCriteria, false)
}

// reconcileClaims moves a match another token recorded as purchased into uncertain: Found there
// could hide a failed purchase (go#711). It does nothing without a token; ok is false when the
// claim read failed.
func (c *Client) reconcileClaims(ctx context.Context, req common.ReconcileRequest, matches, uncertain []string) (m, u []string, ok bool) {
	if req.Options.IdempotencyToken == "" {
		return matches, uncertain, true
	}
	kept, claimed, err := c.unclaimed(ctx, matches, req.Options)
	if err != nil {
		return nil, nil, false
	}
	return kept, append(uncertain, claimed...), true
}

// classifyNodes splits the scanned nodes into full matches and uncertain candidates.
func classifyNodes(nodes []redshifttypes.ReservedNode, offeringID string, count int, nodeType string, months int, lo, hi time.Time) (matches, uncertain []string) {
	for i := range nodes {
		id := aws.ToString(nodes[i].ReservedNodeId)
		switch nodeFit(&nodes[i], offeringID, count, nodeType, months, lo, hi) {
		case fitMatch:
			matches = append(matches, id)
		case fitUncertain:
			uncertain = append(uncertain, id)
		case fitNo:
		}
	}
	return matches, uncertain
}
