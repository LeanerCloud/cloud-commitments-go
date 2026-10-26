package skulist

import (
	"sync"

	"google.golang.org/api/cloudbilling/v1"
)

// Cache lists one Cloud Billing service's SKU catalog once, so that pricing N
// recommendations costs one catalog read instead of N. The zero value is
// ready to use. A failed listing is not cached, so the next call retries.
type Cache struct {
	skus *cloudbilling.ListSkusResponse
	mu   sync.Mutex
}

// Get returns the cached catalog of serviceID, listing it through svc on the
// first successful call. Callers racing on a cold cache wait for one listing.
func (c *Cache) Get(svc BillingService, serviceID string) (*cloudbilling.ListSkusResponse, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.skus != nil {
		return c.skus, nil
	}
	skus, err := svc.ListSKUs(serviceID)
	if err != nil {
		return nil, err
	}
	c.skus = skus
	return skus, nil
}
