package skumatch

import (
	"fmt"

	"google.golang.org/api/cloudbilling/v1"
)

// SlotPrices collects at most one price per slot from the SKUs that matched a
// lookup. The catalog order carries no meaning, so a later SKU must not
// silently replace an earlier one: two matching SKUs that feed the same slot
// at different prices make the lookup ambiguous and Add returns an error.
// SKUs that repeat the same price agree and are accepted. The 1-year and
// 3-year commitment slots are independent, so a catalog with both terms is not
// ambiguous; Commitment selects the one the caller asked for.
type SlotPrices struct {
	onDemandSKU string
	commit1SKU  string
	commit3SKU  string

	OnDemand float64

	commit1Yr float64
	commit3Yr float64

	// The *Set flags record that a slot holds a price. They do not rely on
	// the SKU ID, which the catalog type allows to be empty.
	onDemandSet bool
	commit1Set  bool
	commit3Set  bool
}

// Commitment returns the commitment price collected for termYears (1 or 3), or
// 0 when no SKU of that term matched. Any other term is an error.
func (p *SlotPrices) Commitment(termYears int) (float64, error) {
	switch termYears {
	case 1:
		return p.commit1Yr, nil
	case 3:
		return p.commit3Yr, nil
	}
	return 0, fmt.Errorf("unsupported commitment term %d years, want 1 or 3", termYears)
}

// Add records price for sku in slot. SlotNone is ignored.
func (p *SlotPrices) Add(slot PriceSlot, sku *cloudbilling.Sku, price float64) error {
	switch slot {
	case SlotOnDemand:
		return setOnce(&p.OnDemand, &p.onDemandSKU, &p.onDemandSet, sku, price, "on-demand")
	case SlotCommit1Yr:
		return setOnce(&p.commit1Yr, &p.commit1SKU, &p.commit1Set, sku, price, "1-year commitment")
	case SlotCommit3Yr:
		return setOnce(&p.commit3Yr, &p.commit3SKU, &p.commit3Set, sku, price, "3-year commitment")
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
