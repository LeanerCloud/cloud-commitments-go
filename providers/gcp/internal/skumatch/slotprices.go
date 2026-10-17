package skumatch

import (
	"fmt"

	"google.golang.org/api/cloudbilling/v1"
)

// SlotPrices collects at most one price per slot from the SKUs that matched a
// lookup. The catalog order carries no meaning, so a later SKU must not
// silently replace an earlier one: two matching SKUs that feed the same slot
// at different prices make the lookup ambiguous and Add returns an error.
// SKUs that repeat the same price agree and are accepted.
type SlotPrices struct {
	OnDemand   float64
	Commitment float64

	onDemandSKU   string
	commitmentSKU string
}

// Add records price for sku in slot. SlotNone is ignored.
func (p *SlotPrices) Add(slot PriceSlot, sku *cloudbilling.Sku, price float64) error {
	switch slot {
	case SlotOnDemand:
		return setOnce(&p.OnDemand, &p.onDemandSKU, sku, price, "on-demand")
	case SlotCommitment:
		return setOnce(&p.Commitment, &p.commitmentSKU, sku, price, "commitment")
	}
	return nil
}

func setOnce(value *float64, skuID *string, sku *cloudbilling.Sku, price float64, slotName string) error {
	if *skuID != "" && *value != price {
		return fmt.Errorf("ambiguous catalog: %s SKU %s (%q) prices %v but matching SKU %s prices %v",
			slotName, sku.SkuId, sku.Description, price, *skuID, *value)
	}
	*value, *skuID = price, sku.SkuId
	return nil
}
