package redshift

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/redshift"
	redshifttypes "github.com/aws/aws-sdk-go-v2/service/redshift/types"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
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
// later node or be refused as ambiguous.
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
