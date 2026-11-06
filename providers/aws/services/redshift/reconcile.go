package redshift

import (
	"context"
	"errors"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

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
	if rec.Region != c.region || countErr != nil || termErr != nil {
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

	var matches, uncertain []string
	for i := range nodes {
		id := aws.ToString(nodes[i].ReservedNodeId)
		switch nodeFit(&nodes[i], offeringID, int(count), rec.ResourceType, months, lo, hi) {
		case fitMatch:
			matches = append(matches, id)
		case fitUncertain:
			uncertain = append(uncertain, id)
		case fitNo:
		}
	}
	// Tagged is EC2-only: Redshift reserved nodes cannot carry the idempotency tag, so it is always false.
	return purchasecfg.Decide(matches, uncertain, reconcileCriteria, false)
}
