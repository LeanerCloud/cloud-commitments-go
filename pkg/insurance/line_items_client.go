package insurance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

const lineItemsPageSize int64 = 100

type lineItemsPagination struct {
	NextPage, PreviousPage                       *int64
	Total, TotalPages, Page, FirstPage, LastPage int64
}

func decodePagination(header string, requested int64) (lineItemsPagination, error) {
	var p lineItemsPagination
	var m map[string]json.RawMessage
	if err := decodeOne(strings.NewReader(header), &m); err != nil {
		return p, errors.New("X-Pagination: invalid JSON metadata")
	}
	err := readFields(m, jsonField{out: &p.Total, name: "total", nullable: false}, jsonField{out: &p.TotalPages, name: "total_pages", nullable: false},
		jsonField{out: &p.Page, name: "page", nullable: false}, jsonField{out: &p.FirstPage, name: "first_page", nullable: false}, jsonField{out: &p.LastPage, name: "last_page", nullable: false})
	if err != nil {
		return p, fmt.Errorf("X-Pagination: %w", err)
	}
	for _, f := range []jsonField{{out: &p.NextPage, name: "next_page", nullable: false}, {out: &p.PreviousPage, name: "previous_page", nullable: false}} {
		if _, ok := m[f.name]; ok {
			if err = readFields(m, f); err != nil {
				return p, fmt.Errorf("X-Pagination: %w", err)
			}
		}
	}
	if err = p.validate(requested); err != nil {
		return p, fmt.Errorf("X-Pagination: %w", err)
	}
	return p, nil
}

func (p lineItemsPagination) validate(requested int64) error {
	if p.Total < 0 || p.TotalPages < 0 || p.Page != requested || p.FirstPage != 1 {
		return errors.New("invalid total or page counters")
	}
	if p.Total == 0 {
		return p.validateEmpty()
	}
	pages := p.Total / lineItemsPageSize
	if p.Total%lineItemsPageSize != 0 {
		pages++
	}
	if p.TotalPages != pages || p.LastPage != pages || p.Page > pages {
		return errors.New("inconsistent page totals")
	}
	return p.validateProgress()
}

func (p lineItemsPagination) validateEmpty() error {
	if p.Page != 1 || p.TotalPages > 1 || p.LastPage != p.TotalPages {
		return errors.New("invalid empty pagination")
	}
	return nil
}

func (p lineItemsPagination) validateProgress() error {
	if p.Page < p.TotalPages && p.NextPage != nil && *p.NextPage != p.Page+1 {
		return errors.New("invalid next_page")
	}
	if p.Page > 1 && p.PreviousPage != nil && *p.PreviousPage != p.Page-1 {
		return errors.New("invalid previous_page")
	}
	return nil
}

func (p lineItemsPagination) rowCount() int64 {
	if p.Total == 0 {
		return 0
	}
	if p.Page == p.TotalPages && p.Total%lineItemsPageSize != 0 {
		return p.Total % lineItemsPageSize
	}
	return lineItemsPageSize
}

// LineItems returns all paginated rows or an error; partial pages never escape.
func (c *Client) LineItems(ctx context.Context, planID string) ([]LineItem, error) {
	if !isUUID(planID) {
		return nil, errors.New("archera plan ID must be a UUID")
	}
	rows := make([]LineItem, 0)
	seen := make(map[string]bool)
	remaining := int64(maxResponseBytes)
	var first lineItemsPagination
	for page := int64(1); ; page++ {
		items, metadata, err := c.lineItemsPage(ctx, planID, page, &remaining)
		if err != nil {
			return nil, err
		}
		if page == 1 {
			first = metadata
		}
		if first.Total != metadata.Total || first.TotalPages != metadata.TotalPages {
			return nil, errors.New("X-Pagination: totals changed between pages")
		}
		for i := range items {
			id := strings.ToLower(items[i].ID)
			if seen[id] {
				return nil, errors.New("duplicate line item id across pages")
			}
			seen[id] = true
			rows = append(rows, items[i])
		}
		if page >= first.TotalPages {
			return rows, nil
		}
	}
}

func (c *Client) lineItemsPage(ctx context.Context, planID string, page int64, remaining *int64) ([]LineItem, lineItemsPagination, error) {
	q := url.Values{"page": {strconv.FormatInt(page, 10)}, "page_size": {"100"}, "order_by": {"id"}}
	body, headers, _, err := c.getWithHeaders(ctx, c.planPath(planID)+"/line-items", q)
	if err != nil {
		return nil, lineItemsPagination{}, err
	}
	defer func() { _ = body.Close() }()
	metadata, err := decodePagination(headers.Get("X-Pagination"), page)
	if err != nil {
		return nil, metadata, err
	}
	r := &boundedReader{r: body, n: *remaining}
	items, err := DecodeLineItems(r, planID)
	*remaining = r.n
	if err != nil {
		return nil, metadata, sanitizeDecodeError(err, c.redact)
	}
	if int64(len(items)) != metadata.rowCount() {
		return nil, metadata, errors.New("X-Pagination: line item count disagrees with metadata")
	}
	return items, metadata, nil
}
