package recommendations

import (
	"context"
	"fmt"
)

// Runaway guards, not size limits. No Cost Explorer input sets MaxResults, so
// CE picks the page size; these only stop a token loop that never ends. One
// over-cap pool aborts the whole GetRICoverageMap survey (coverage.go).
const (
	maxCoveragePages    = 100
	maxUtilizationPages = 100
)

// paginateCE drives a Cost Explorer NextPageToken loop. page fetches one page
// for the given token and returns the next token; a nil or empty token ends
// the loop. Each iteration returns ctx.Err() raw, then enforces maxPages, so a
// runaway loop errors instead of returning partial results as complete.
func paginateCE(ctx context.Context, maxPages int, what string, page func(token *string) (next *string, err error)) error {
	var token *string
	for n := 0; ; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if n >= maxPages {
			return fmt.Errorf("%s: pagination cap reached after %d pages", what, maxPages)
		}
		next, err := page(token)
		if err != nil {
			return err
		}
		if next == nil || *next == "" {
			return nil
		}
		token = next
	}
}
