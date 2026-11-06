package redshift

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/internal/purchasecfg"
)

const (
	// maxReservedNodePages bounds the dedupe scan. An incomplete scan refuses
	// the purchase rather than reporting no match.
	maxReservedNodePages = 10
	// redshiftClockSkew is the slack between the caller's RequestedAt and AWS's
	// node StartTime. The window deliberately has no upper bound: a late
	// StartTime must never cause a double buy.
	redshiftClockSkew = 5 * time.Minute
	// recoverLookupTimeout bounds the post-error lookup, which runs on a
	// context detached from the caller's cancellation.
	recoverLookupTimeout = 30 * time.Second
)

// errRequestedAtRequired refuses a tokenized purchase that cannot be matched.
var errRequestedAtRequired = errors.New("RequestedAt is required with an idempotency token: Redshift reserved nodes cannot be tagged, so an earlier purchase is found by start time; refusing to purchase to avoid a possible double-buy")

// findMatchingNodes lists the IDs of owned reserved nodes with the given
// offering ID and node count whose StartTime is not before requestedAt minus
// redshiftClockSkew. A node missing StartTime, offering ID or
// node count counts as a match on that field (fail closed). The offering ID fixes node type, term and offering type.
//
// Caller contract: because the window is open-ended, the caller must not
// dispatch another Redshift purchase for the same account, region, offering ID
// and node count while a re-drive of an earlier lineage is still possible (the
// platform's execution claim provides this). Otherwise a re-drive may adopt the
// later node or be refused as ambiguous. A caller that re-dispatches a token
// whose earlier attempt may have been sent must first wait long enough for
// DescribeReservedNodes to list a node that attempt bought; an AttemptJournal
// (go#107) records the attempts but cannot close that listing lag.
func (c *Client) findMatchingNodes(ctx context.Context, offeringID string, count int, requestedAt time.Time) ([]string, error) {
	since := requestedAt.Add(-redshiftClockSkew)
	var ids []string
	var marker *string
	for page := 0; page < maxReservedNodePages; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		response, err := c.client.DescribeReservedNodes(ctx, &redshift.DescribeReservedNodesInput{
			Marker:     marker,
			MaxRecords: aws.Int32(100),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to describe reserved nodes for dedupe: %w", err)
		}
		for i := range response.ReservedNodes {
			if nodeMatches(&response.ReservedNodes[i], offeringID, count, since) {
				ids = append(ids, aws.ToString(response.ReservedNodes[i].ReservedNodeId))
			}
		}
		if aws.ToString(response.Marker) == "" {
			return ids, nil
		}
		marker = response.Marker
	}
	return nil, fmt.Errorf("pagination cap reached after %d pages for Redshift reserved-node dedupe lookup", maxReservedNodePages)
}

func nodeMatches(node *redshifttypes.ReservedNode, offeringID string, count int, since time.Time) bool {
	if _, owned := ownedNodeState(node.State); !owned {
		return false
	}
	if node.ReservedNodeOfferingId != nil && *node.ReservedNodeOfferingId != offeringID {
		return false
	}
	if node.NodeCount != nil && int(*node.NodeCount) != count {
		return false
	}
	return node.StartTime == nil || !node.StartTime.Before(since)
}

// guardAgainstRepurchase runs before a tokenized purchase. It reports done
// when the purchase must not proceed: result then carries either the adopted
// node (Success) or the refusal in result.Error.
func (c *Client) guardAgainstRepurchase(ctx context.Context, result *common.PurchaseResult, offeringID string, count int, opts common.PurchaseOptions) (done bool) {
	if opts.RequestedAt.IsZero() {
		result.Error = errRequestedAtRequired
		return true
	}
	ids, err := c.findMatchingNodes(ctx, offeringID, count, opts.RequestedAt)
	switch {
	case err != nil:
		result.Error = fmt.Errorf("idempotency lookup failed before Redshift purchase (refusing to purchase to avoid a possible double-buy): %w", err)
	case len(ids) > 1:
		result.Error = fmt.Errorf("ambiguous Redshift dedupe: %d reserved nodes (%s) match offering %s, node count %d, started since %s; refusing to purchase to avoid a possible double-buy; check the account and, if the commitment is missing, start a new purchase",
			len(ids), strings.Join(ids, ", "), offeringID, count, opts.RequestedAt.Add(-redshiftClockSkew).UTC().Format(time.RFC3339))
	case len(ids) == 1:
		log.Printf("Redshift reserved node %s already matches idempotency token %s; skipping purchase", ids[0], common.MaskToken(opts.IdempotencyToken))
		adopt(result, ids[0])
		return true
	default:
		return false
	}
	return true
}

// adoptAfterUnknownOutcome runs after a purchase error. When the outcome is
// unknown and exactly one node matches, the purchase did happen: adopt it. A
// missing or ambiguous match leaves the original error in place.
func (c *Client) adoptAfterUnknownOutcome(ctx context.Context, result *common.PurchaseResult, offeringID string, count int, opts common.PurchaseOptions) bool {
	if !errors.Is(result.Error, common.ErrOutcomeUnknown) || opts.IdempotencyToken == "" || opts.RequestedAt.IsZero() {
		return false
	}
	lookupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoverLookupTimeout)
	defer cancel()
	ids, err := c.findMatchingNodes(lookupCtx, offeringID, count, opts.RequestedAt)
	if err != nil || len(ids) != 1 {
		log.Printf("Redshift purchase outcome unknown and not recoverable (matches=%d, lookup error=%v)", len(ids), err)
		return false
	}
	log.Printf("Redshift purchase for token %s had an unknown outcome; adopted matching node %s", common.MaskToken(opts.IdempotencyToken), ids[0])
	adopt(result, ids[0])
	return true
}

func adopt(result *common.PurchaseResult, nodeID string) {
	result.Success = true
	result.ExistingCommitment = true
	result.CommitmentID = nodeID
	result.Error = nil
}

// journalActive reports whether the attempt journal governs this purchase
// (go#107). Without a token or a journal the flow is the window dedupe alone.
func journalActive(opts common.PurchaseOptions) bool {
	return opts.IdempotencyToken != "" && opts.AttemptJournal != nil
}

// guardBeforePurchase runs the pre-send dedupe for a tokenized purchase and
// reports done when the purchase must not proceed (result carries the outcome).
func (c *Client) guardBeforePurchase(ctx context.Context, result *common.PurchaseResult, offeringID string, count int, opts common.PurchaseOptions) (done bool) {
	switch {
	case journalActive(opts):
		return c.journalPreflight(ctx, result, offeringID, count, opts)
	case opts.IdempotencyToken != "":
		return c.guardAgainstRepurchase(ctx, result, offeringID, count, opts)
	default:
		return false
	}
}

func refuseBeforePurchase(result *common.PurchaseResult, format string, args ...any) {
	result.Error = fmt.Errorf("%w: "+format, append([]any{common.ErrRefusedBeforePurchase}, args...)...)
}

// journalPreflight is the journal path of the pre-send dedupe: the caller's
// recorded attempts first, then the start-time window.
func (c *Client) journalPreflight(ctx context.Context, result *common.PurchaseResult, offeringID string, count int, opts common.PurchaseOptions) (done bool) {
	if opts.RequestedAt.IsZero() {
		result.Error = errRequestedAtRequired
		return true
	}
	if opts.ExecutionID == "" {
		refuseBeforePurchase(result, "an execution ID is required with an attempt journal")
		return true
	}
	priors, err := opts.AttemptJournal.PriorAttempts(ctx, opts.IdempotencyToken)
	if err != nil {
		refuseBeforePurchase(result, "attempt journal lookup failed for token %s: %w", common.MaskToken(opts.IdempotencyToken), err)
		return true
	}
	if ids := recordedReservationIDs(priors); len(ids) > 0 {
		c.adoptRecordedNode(ctx, result, ids, offeringID, count, opts)
		return true
	}
	if c.guardAgainstRepurchase(ctx, result, offeringID, count, opts) {
		if result.Success {
			c.recordPurchased(ctx, result, offeringID, opts)
		}
		return true
	}
	return refuseUnresolvedPriors(result, priors, offeringID, opts)
}

// recordedReservationIDs returns the distinct reservation IDs of Purchased
// records, in first-seen order.
func recordedReservationIDs(priors []common.AttemptRecord) []string {
	var ids []string
	for _, p := range priors {
		if p.State == common.AttemptPurchased && !slices.Contains(ids, p.ReservationID) {
			ids = append(ids, p.ReservationID)
		}
	}
	return ids
}

// adoptRecordedNode adopts the node an earlier attempt recorded as bought,
// whatever else the window matches, once the account still lists it. It writes
// nothing: the existing Purchased record already answers later attempts.
func (c *Client) adoptRecordedNode(ctx context.Context, result *common.PurchaseResult, ids []string, offeringID string, count int, opts common.PurchaseOptions) {
	if len(ids) > 1 {
		refuseBeforePurchase(result, "attempt journal is inconsistent for token %s: %d distinct reservation IDs recorded as purchased", common.MaskToken(opts.IdempotencyToken), len(ids))
		return
	}
	matches, err := c.findMatchingNodes(ctx, offeringID, count, opts.RequestedAt)
	switch {
	case err != nil:
		refuseBeforePurchase(result, "lookup of recorded reserved node %s failed: %w", ids[0], err)
	case !slices.Contains(matches, ids[0]):
		refuseBeforePurchase(result, "reserved node %s recorded for token %s is not among the matching owned nodes; check the account before buying again", ids[0], common.MaskToken(opts.IdempotencyToken))
	default:
		log.Printf("Redshift reserved node %s is recorded for token %s; skipping purchase", ids[0], common.MaskToken(opts.IdempotencyToken))
		adopt(result, ids[0])
	}
}

// refuseUnresolvedPriors refuses when an earlier attempt may have sent a buy
// that this attempt could not recognize: the caller's own sending or
// outcome_unknown record, or another execution's record taken under an offering
// that differs from the current one (or has none).
func refuseUnresolvedPriors(result *common.PurchaseResult, priors []common.AttemptRecord, offeringID string, opts common.PurchaseOptions) (done bool) {
	for _, p := range priors {
		if p.State != common.AttemptSending && p.State != common.AttemptOutcomeUnknown {
			continue
		}
		switch {
		case p.ExecutionID == opts.ExecutionID:
			result.Error = fmt.Errorf("an earlier attempt of this execution for token %s is %s and no matching node was found: %w; check the account before buying again",
				common.MaskToken(opts.IdempotencyToken), p.State, common.ErrOutcomeUnknown)
			return true
		case p.OfferingID != offeringID:
			result.Error = fmt.Errorf("an earlier attempt for token %s under offering %q may have bought a node that the current offering %q cannot match: %w; check the account before buying again",
				common.MaskToken(opts.IdempotencyToken), p.OfferingID, offeringID, common.ErrOutcomeUnknown)
			return true
		}
	}
	return false
}

// recordSending writes the pre-send record on the journal path. It reports
// false, with the refusal in result.Error, when the record is not durable: no
// record, no buy. Without a journal it does nothing and reports true.
func (c *Client) recordSending(ctx context.Context, result *common.PurchaseResult, offeringID string, opts common.PurchaseOptions) bool {
	if !journalActive(opts) {
		return true
	}
	err := opts.AttemptJournal.RecordAttempt(ctx, common.AttemptRecord{
		IdempotencyToken: opts.IdempotencyToken, ExecutionID: opts.ExecutionID,
		State: common.AttemptSending, OfferingID: offeringID,
	})
	if err != nil {
		refuseBeforePurchase(result, "could not record the purchase attempt for token %s: %w", common.MaskToken(opts.IdempotencyToken), err)
		return false
	}
	return true
}

// writeAttempt writes a record on a context detached from the caller's
// cancellation: the buy may already have happened.
func writeAttempt(ctx context.Context, opts common.PurchaseOptions, state common.AttemptState, reservationID, offeringID string) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recoverLookupTimeout)
	defer cancel()
	return opts.AttemptJournal.RecordAttempt(writeCtx, common.AttemptRecord{
		IdempotencyToken: opts.IdempotencyToken, ExecutionID: opts.ExecutionID,
		State: state, ReservationID: reservationID, OfferingID: offeringID,
	})
}

// recordPurchased records result.CommitmentID as bought. A node that exists but
// is not recorded is never a Success: the error names the ID and wraps
// ErrOutcomeUnknown, and CommitmentID stays set.
func (c *Client) recordPurchased(ctx context.Context, result *common.PurchaseResult, offeringID string, opts common.PurchaseOptions) {
	err := writeAttempt(ctx, opts, common.AttemptPurchased, result.CommitmentID, offeringID)
	if err == nil {
		return
	}
	log.Printf("Redshift reserved node %s was bought for token %s but the attempt record failed: %v", result.CommitmentID, common.MaskToken(opts.IdempotencyToken), err)
	result.Success = false
	result.ExistingCommitment = false
	result.Error = fmt.Errorf("redshift reserved node %s was bought but not recorded: %w: %w", result.CommitmentID, err, common.ErrOutcomeUnknown)
}

// recordUnknown records a send whose outcome could not be resolved. The
// original error is returned either way.
func recordUnknown(ctx context.Context, result *common.PurchaseResult, offeringID string, opts common.PurchaseOptions) {
	if err := writeAttempt(ctx, opts, common.AttemptOutcomeUnknown, "", offeringID); err != nil {
		result.Error = fmt.Errorf("%w; additionally the outcome_unknown attempt record could not be written: %w", result.Error, err)
	}
}

// finishJournaled turns the buy response into a result and records the outcome.
func (c *Client) finishJournaled(ctx context.Context, result common.PurchaseResult, response *redshift.PurchaseReservedNodeOfferingOutput, sendErr error, offeringID string, count int, opts common.PurchaseOptions) (common.PurchaseResult, error) {
	switch {
	case sendErr != nil:
		result.Error = fmt.Errorf("failed to purchase Redshift Reserved Node: %w", purchasecfg.ClassifyPurchaseError(sendErr))
	case response == nil || response.ReservedNode == nil:
		result.Error = fmt.Errorf("purchase response was empty: %w", common.ErrOutcomeUnknown)
	default:
		result.Success = true
		result.CommitmentID = aws.ToString(response.ReservedNode.ReservedNodeId)
		result.Cost = reservedNodeTotal(response.ReservedNode)
		c.recordPurchased(ctx, &result, offeringID, opts)
		return result, result.Error
	}
	if c.adoptAfterUnknownOutcome(ctx, &result, offeringID, count, opts) {
		c.recordPurchased(ctx, &result, offeringID, opts)
		return result, result.Error
	}
	recordUnknown(ctx, &result, offeringID, opts)
	return result, result.Error
}
