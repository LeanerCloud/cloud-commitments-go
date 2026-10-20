package pricing

import (
	"fmt"
	"strings"
)

type RedisIdentity struct {
	ArmSKUName, SKUName, ProductName, MeterName string
}

// ParseRedisIdentity maps a Redis reservation SKU to its exact Retail Prices
// identity. It accepts only the legacy Premium (P1-P5), Enterprise (E*) and
// Enterprise Flash (F*) families.
//
// Newer Azure_Managed_Redis_* identities (for example
// Azure_Managed_Redis_Balanced_B10) are deliberately unsupported: they return
// an "unsupported Redis reservation SKU" error before any quote request is
// built, so callers never price them from a legacy filter. Adding a parser
// prefix alone would not make them supported, because discovery,
// recommendation mapping and quote identity must agree first. Supporting them
// is tracked in #188.
func ParseRedisIdentity(sku string) (RedisIdentity, error) {
	arm := sku
	if strings.HasPrefix(arm, "Premium_") {
		arm = "Azure_Redis_Cache_" + arm + "_Cache"
	}
	var display, product string
	meterSuffix := " Cache"
	switch {
	case strings.HasPrefix(arm, "Azure_Redis_Cache_Premium_"):
		display = strings.TrimPrefix(arm, "Azure_Redis_Cache_Premium_")
		var ok bool
		display, ok = strings.CutSuffix(display, "_Cache")
		if !ok {
			return RedisIdentity{}, fmt.Errorf("unsupported Redis reservation SKU: %q", sku)
		}
		switch display {
		case "P1", "P2", "P3", "P4", "P5":
		default:
			return RedisIdentity{}, fmt.Errorf("unsupported Redis reservation SKU: %q", sku)
		}
		product, meterSuffix = "Azure Redis Cache Premium", " Cache Instance"
	case strings.HasPrefix(arm, "Azure_Redis_Cache_Enterprise_Flash_F"):
		display = "F" + strings.TrimPrefix(arm, "Azure_Redis_Cache_Enterprise_Flash_F")
		product = "Azure Redis Cache Enterprise Flash"
	case strings.HasPrefix(arm, "Azure_Redis_Cache_Enterprise_E"):
		display = "E" + strings.TrimPrefix(arm, "Azure_Redis_Cache_Enterprise_E")
		product = "Azure Redis Cache Enterprise"
	default:
		return RedisIdentity{}, fmt.Errorf("unsupported Redis reservation SKU: %q", sku)
	}
	if len(display) == 1 || strings.Trim(display[1:], "0123456789") != "" {
		return RedisIdentity{}, fmt.Errorf("unsupported Redis reservation SKU: %q", sku)
	}
	return RedisIdentity{arm, display, product, display + meterSuffix}, nil
}

func (id RedisIdentity) Matches(item RetailPriceItem, region string) bool {
	return item.ServiceName == "Redis Cache" && item.ArmRegionName == region &&
		item.ArmSKUName == id.ArmSKUName && item.SKUName == id.SKUName &&
		item.ProductName == id.ProductName && item.MeterName == id.MeterName
}
