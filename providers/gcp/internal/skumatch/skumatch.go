// Package skumatch holds the matching rules the GCP services share when they
// pick a price out of Cloud Billing catalog SKUs.
package skumatch

import (
	"strings"

	"google.golang.org/api/cloudbilling/v1"
)

// InRegion reports whether the SKU lists region in ServiceRegions. A SKU with
// no regions is not treated as global: the API documents ServiceRegions only
// as the regions a SKU is offered at, so missing data must not price every region.
func InRegion(sku *cloudbilling.Sku, region string) bool {
	for _, serviceRegion := range sku.ServiceRegions {
		if strings.EqualFold(serviceRegion, region) {
			return true
		}
	}
	return false
}

// HasToken reports whether the SKU description contains token as a whole
// identifier, case-insensitively. "db-n1-standard-1" does not match inside
// "db-n1-standard-16" and "STANDARD" does not match inside "STANDARD_HA".
func HasToken(description, token string) bool {
	if token == "" {
		return false
	}
	description, token = strings.ToLower(description), strings.ToLower(token)
	for offset := 0; ; {
		i := strings.Index(description[offset:], token)
		if i < 0 {
			return false
		}
		start := offset + i
		end := start + len(token)
		if (start == 0 || !isTokenChar(description[start-1])) &&
			(end == len(description) || !isTokenChar(description[end])) {
			return true
		}
		offset = start + 1
	}
}

func isTokenChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' || b == '-'
}
