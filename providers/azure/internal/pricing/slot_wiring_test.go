package pricing

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/providers/azure/internal/slot/slottest"
)

type checkingHTTPClient struct{ check func() }

func (c checkingHTTPClient) Do(*http.Request) (*http.Response, error) {
	c.check()
	return okJSONResponse(`{"Items":[],"NextPageLink":"","Count":0}`), nil
}

func TestFetchAll_HoldsSlot(t *testing.T) {
	p, ctx := slottest.New(t)
	_, err := FetchAll[fakeItem](ctx, checkingHTTPClient{p.Check}, "https://prices.example/p", DefaultPageTimeout, DefaultMaxPages)
	require.NoError(t, err)
	p.RequireCalled()
	p.Free()
}
