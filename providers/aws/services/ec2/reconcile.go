package ec2

import (
	"context"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/offeringprice"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

var reconcileCriteria = []string{
	"instance-type", "count", "product", "tenancy", "scope", "zone",
	"duration", "offering-type", "offering-class", "start-window",
}

// reconcileQuery validates the request and builds the lookup query; reason is non-empty on refusal.
func (c *Client) reconcileQuery(req common.ReconcileRequest) (q ec2OfferingQuery, count int, lo, hi time.Time, reason string) {
	lo, hi, ok := purchasecfg.ReconcileWindow(req.Options.RequestedAt, req.AttemptEndedAt)
	if !ok {
		return q, 0, lo, hi, purchasecfg.ReasonInvalidWindow
	}
	rec := req.Recommendation
	n, err := offeringprice.PurchaseCount(rec.Count)
	if rec.Region != c.region || err != nil {
		return q, 0, lo, hi, purchasecfg.ReasonInvalidRequest
	}
	if q, err = c.buildEC2QueryFromRec(rec); err != nil {
		return q, 0, lo, hi, purchasecfg.ReasonInvalidRequest
	}
	// Same class resolution as findOffering, so reconcile matches what the purchase asked for.
	if q.offeringClass, err = resolveOfferingClassType(req.Options.OfferingClass); err != nil {
		return q, 0, lo, hi, purchasecfg.ReasonInvalidRequest
	}
	return q, int(n), lo, hi, ""
}

// ReconcilePurchase reports, read-only, whether a purchase that returned ErrOutcomeUnknown landed.
// Any doubt or failure is Unknown; see purchasecfg.Decide.
func (c *Client) ReconcilePurchase(ctx context.Context, req common.ReconcileRequest) common.ReconcileResult {
	q, count, lo, hi, reason := c.reconcileQuery(req)
	if reason != "" {
		return purchasecfg.UnknownResult(reason)
	}

	if ctx.Err() != nil {
		return purchasecfg.UnknownResult(purchasecfg.ReasonLookupFailed)
	}
	out, err := c.client.DescribeReservedInstances(ctx, &ec2.DescribeReservedInstancesInput{})
	if err != nil || out == nil {
		return purchasecfg.UnknownResult(purchasecfg.ReasonLookupFailed)
	}

	var matches, uncertain []string
	tagged := false
	for i := range out.ReservedInstances {
		ri := out.ReservedInstances[i]
		id := aws.ToString(ri.ReservedInstancesId)
		f, isTagged := riFit(ri, q, count, req.Options.IdempotencyToken, lo, hi)
		switch f {
		case fitMatch:
			matches = append(matches, id)
			tagged = isTagged
		case fitUncertain:
			uncertain = append(uncertain, id)
		case fitNo:
		}
	}
	// Tagged is EC2-only: Redshift nodes cannot carry the idempotency tag.
	return purchasecfg.Decide(matches, uncertain, reconcileCriteria, tagged)
}
