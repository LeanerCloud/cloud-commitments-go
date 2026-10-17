// Package purchaseguard holds the purchase idempotency guard shared by the AWS
// services whose reservations are keyed by a caller-supplied ID (issue #641):
// look the reservation up before buying, and recover when AWS rejects the
// duplicate ID.
package purchaseguard

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// Finder looks up a nonterminal reservation by the ID or name being purchased.
type Finder func(ctx context.Context) (id string, found bool, err error)

// Guard short-circuits a re-drive (issue #641): with a token set it reports
// (existingID, true, nil) if find locates the reservation, ("", false, nil) for
// a first-time purchase, or a fail-loud error on lookup failure. With an empty
// token it is a no-op and never calls find.
func Guard(ctx context.Context, token, label string, find Finder) (existingID string, found bool, err error) {
	if token == "" {
		return "", false, nil
	}
	existingID, found, lookupErr := find(ctx)
	if lookupErr != nil {
		return "", false, fmt.Errorf("idempotency lookup failed before %s purchase (refusing to purchase to avoid a possible double-buy): %w", label, lookupErr)
	}
	if found {
		log.Printf("%s for idempotency token %s already exists (%s); skipping purchase (issue #641 re-drive)", label, common.MaskToken(token), existingID)
		return existingID, true, nil
	}
	return "", false, nil
}

// Recover handles the native server-side dedupe backstop (issue #641): if the
// guard missed the existing reservation but purchaseErr is the service's
// AlreadyExists fault F (a pointer type, matched with errors.As), it re-looks
// it up and returns (existingID, true). A lookup error or a miss returns
// ("", false) so the caller reports the original purchase error.
func Recover[F error](ctx context.Context, token, label string, purchaseErr error, find Finder) (string, bool) {
	if token == "" {
		return "", false
	}
	var already F
	if !errors.As(purchaseErr, &already) {
		return "", false
	}
	existingID, found, lookupErr := find(ctx)
	if lookupErr == nil && found {
		log.Printf("%s %s already existed at purchase time; treating as idempotent re-drive (issue #641)", label, existingID)
		return existingID, true
	}
	return "", false
}
