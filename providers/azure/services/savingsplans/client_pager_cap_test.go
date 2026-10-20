package savingsplans

import (
	"context"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/billingbenefits/armbillingbenefits"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pagedListAllPager serves limit pages; onCall runs before the n-th page (1-based).
type pagedListAllPager struct {
	calls, limit int
	onCall       func(n int)
	page         func(n int) []*armbillingbenefits.SavingsPlanModel
}

func (p *pagedListAllPager) More() bool { return p.calls < p.limit }

func (p *pagedListAllPager) NextPage(_ context.Context) (armbillingbenefits.SavingsPlanClientListAllResponse, error) {
	p.calls++
	if p.onCall != nil {
		p.onCall(p.calls)
	}
	var resp armbillingbenefits.SavingsPlanClientListAllResponse
	if p.page != nil {
		resp.Value = p.page(p.calls)
	}
	return resp, nil
}

func ownedPlan(id string) *armbillingbenefits.SavingsPlanModel {
	return &armbillingbenefits.SavingsPlanModel{
		ID:         &id,
		SKU:        &armbillingbenefits.SKU{Name: toPtr("Compute_Savings_Plan")},
		Properties: &armbillingbenefits.SavingsPlanModelProperties{BillingScopeID: toPtr("/subscriptions/sub-abc"), EffectiveDateTime: toPtr(time.Now())},
	}
}

func TestGetExistingCommitments_PageCap(t *testing.T) {
	t.Parallel()
	t.Run("endless pager errors at the cap", func(t *testing.T) {
		pager := &pagedListAllPager{limit: 10000}
		c := NewClient(nil, "sub-abc", "eastus")
		c.SetListAllPager(pager)

		got, err := c.GetExistingCommitments(context.Background())
		require.Error(t, err)
		assert.Nil(t, got)
		assert.Contains(t, err.Error(), "pagination cap")
		assert.Equal(t, maxSavingsPlanPages, pager.calls)
	})

	t.Run("cancel mid-walk returns the context error", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pager := &pagedListAllPager{limit: 10000, onCall: func(n int) {
			if n == 2 {
				cancel()
			}
		}}
		c := NewClient(nil, "sub-abc", "eastus")
		c.SetListAllPager(pager)

		got, err := c.GetExistingCommitments(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, got)
		assert.Equal(t, 2, pager.calls)
	})

	t.Run("already-canceled ctx fails before any page", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		pager := &pagedListAllPager{limit: 1}
		c := NewClient(nil, "sub-abc", "eastus")
		c.SetListAllPager(pager)

		got, err := c.GetExistingCommitments(ctx)
		require.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, got)
		assert.Equal(t, 0, pager.calls)
	})

	t.Run("multi-page listing returns every plan", func(t *testing.T) {
		pager := &pagedListAllPager{limit: 3, page: func(n int) []*armbillingbenefits.SavingsPlanModel {
			return []*armbillingbenefits.SavingsPlanModel{ownedPlan("sp-" + string(rune('a'+n)))}
		}}
		c := NewClient(nil, "sub-abc", "eastus")
		c.SetListAllPager(pager)

		got, err := c.GetExistingCommitments(context.Background())
		require.NoError(t, err)
		assert.Len(t, got, 3)
	})

	t.Run("exactly cap pages succeed", func(t *testing.T) {
		pager := &pagedListAllPager{limit: maxSavingsPlanPages}
		c := NewClient(nil, "sub-abc", "eastus")
		c.SetListAllPager(pager)

		_, err := c.GetExistingCommitments(context.Background())
		require.NoError(t, err)
		assert.Equal(t, maxSavingsPlanPages, pager.calls)
	})
}
