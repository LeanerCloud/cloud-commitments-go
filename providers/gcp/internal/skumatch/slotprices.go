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
	onDemandSKU   string
	commitmentSKU string

	OnDemand   float64
	Commitment float64

	// onDemandSet and commitmentSet record that a slot holds a price. They
	// do not rely on the SKU ID, which the catalog type allows to be empty.
	onDemandSet   bool
	commitmentSet bool
}

// Add records price for sku in slot. SlotNone is ignored.
func (p *SlotPrices) Add(slot PriceSlot, sku *cloudbilling.Sku, price float64) error {
	switch slot {
	case SlotOnDemand:
		return setOnce(&p.OnDemand, &p.onDemandSKU, &p.onDemandSet, sku, price, "on-demand")
	case SlotCommitment:
		return setOnce(&p.Commitment, &p.commitmentSKU, &p.commitmentSet, sku, price, "commitment")
	}
	return nil
}

func setOnce(value *float64, skuID *string, set *bool, sku *cloudbilling.Sku, price float64, slotName string) error {
	if *set && *value != price {
		return fmt.Errorf("ambiguous catalog: %s SKU %s (%q) prices %v but matching SKU %s prices %v",
			slotName, sku.SkuId, sku.Description, price, *skuID, *value)
	}
	*value, *skuID, *set = price, sku.SkuId, true
	return nil
}
