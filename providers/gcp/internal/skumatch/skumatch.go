// Package skumatch holds the matching rules the GCP services share when they
// pick a price out of Cloud Billing catalog SKUs.
package skumatch

import (
	"fmt"
	"strings"

	"google.golang.org/api/cloudbilling/v1"
)

// geoTaxonomyGlobal is the GeoTaxonomy.Type of a SKU that applies to every region.
const geoTaxonomyGlobal = "GLOBAL"

// InRegion reports whether the SKU applies in region. A SKU with named regions
// applies only there. A SKU with no regions applies everywhere only when its
// GeoTaxonomy.Type is GLOBAL (the catalog leaves the region list empty for
// global SKUs); empty regions with any other or a missing taxonomy is
// ambiguous data and does not match.
func InRegion(sku *cloudbilling.Sku, region string) bool {
	if len(sku.ServiceRegions) == 0 {
		return sku.GeoTaxonomy != nil && sku.GeoTaxonomy.Type == geoTaxonomyGlobal
	}
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

// PriceSlot says which price a SKU feeds.
type PriceSlot int

const (
	SlotOnDemand PriceSlot = iota
	SlotCommitment
	// SlotNone marks SKUs that are neither on-demand nor committed-use prices.
	SlotNone
)

// Category.UsageType values the catalog uses.
const (
	usageOnDemand    = "OnDemand"
	usageCommit1Yr   = "Commit1Yr"
	usageCommit3Yr   = "Commit3Yr"
	usagePreemptible = "Preemptible"
	usageSpot        = "Spot"
)

// Slot classifies the SKU by Category.UsageType. Known values: OnDemand,
// Commit1Yr, Commit3Yr, Preemptible, Spot. Preemptible and Spot SKUs are
// SlotNone. A missing category or any other usage type (Commit1Mo currently
// included) is an error rather than a guess, because a wrong slot silently
// mislabels the commitment price.
//
// Known gap: Commit1Yr and Commit3Yr share SlotCommitment, so callers that see
// both terms keep whichever SKU comes last.
func Slot(sku *cloudbilling.Sku) (PriceSlot, error) {
	if sku.Category == nil {
		return 0, fmt.Errorf("sku %s has no category, cannot tell commitment from on-demand", sku.SkuId)
	}
	switch sku.Category.UsageType {
	case usageOnDemand:
		return SlotOnDemand, nil
	case usageCommit1Yr, usageCommit3Yr:
		return SlotCommitment, nil
	case usagePreemptible, usageSpot:
		return SlotNone, nil
	}
	return 0, fmt.Errorf("sku %s has unrecognized usage type %q", sku.SkuId, sku.Category.UsageType)
}
