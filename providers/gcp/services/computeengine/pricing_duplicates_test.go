package computeengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/cloudbilling/v1"
)

// memoryOptimizedUSEast4SKUs: live-shaped us-east4 rows (read 2026-11-13). The
// catalog lists each on-demand Memory-optimized core and RAM SKU twice, under
// "Northern Virginia" and "Virginia", with no M1 or M2 label; the pairs differ by
// under 1%. Commitments are one pair.
func memoryOptimizedUSEast4SKUs(coreAlt, ramAlt int64) []*cloudbilling.Sku {
	east4 := []string{"us-east4"}
	return []*cloudbilling.Sku{
		catalogSKU("28D5-8C18-1FE2", "Memory-optimized Instance Core running in Northern Virginia", "CPU", "OnDemand", "h", 39200000, east4),
		catalogSKU("C208-228E-1A82", "Memory-optimized Instance Core running in Virginia", "CPU", "OnDemand", "h", coreAlt, east4),
		catalogSKU("2525-4ED2-E479", "Memory-optimized Instance Ram running in Northern Virginia", "RAM", "OnDemand", "GiBy.h", 5800000, east4),
		catalogSKU("A09C-017D-4EEB", "Memory-optimized Instance Ram running in Virginia", "RAM", "OnDemand", "GiBy.h", ramAlt, east4),
		catalogSKU("m-c1-core", "Commitment v1: Memory-optimized Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 25000000, east4),
		catalogSKU("m-c1-ram", "Commitment v1: Memory-optimized Ram in Americas for 1 Year", "RAM", "Commit1Yr", "GiBy.h", 3400000, east4),
	}
}

func TestFindFamilyRates_NearDuplicateOnDemandPairKeepsHigherPrice(t *testing.T) {
	t.Parallel()
	for _, machineType := range []string{"m1-megamem-96", "m2-ultramem-208"} {
		skus := memoryOptimizedUSEast4SKUs(39232200, 5759200)
		rates, err := findFamilyRates(skus, machineType, "us-east4", 1)
		require.NoError(t, err, machineType)
		assert.InDelta(t, 0.0392322, rates.onDemand.cpu, 1e-12, "core: higher of 0.0392 and 0.0392322")
		assert.InDelta(t, 0.0058, rates.onDemand.ram, 1e-12, "ram: higher of 0.0058 and 0.0057592")
	}
}

func TestFindFamilyRates_NearDuplicateOrderDoesNotMatter(t *testing.T) {
	t.Parallel()
	skus := memoryOptimizedUSEast4SKUs(39232200, 5759200)
	skus[0], skus[1] = skus[1], skus[0]
	skus[2], skus[3] = skus[3], skus[2]
	rates, err := findFamilyRates(skus, "m1-megamem-96", "us-east4", 1)
	require.NoError(t, err)
	assert.InDelta(t, 0.0392322, rates.onDemand.cpu, 1e-12)
	assert.InDelta(t, 0.0058, rates.onDemand.ram, 1e-12)
}

func TestFindFamilyRates_WideOnDemandSpreadStaysAmbiguous(t *testing.T) {
	t.Parallel()
	// 5% apart on the core, and 1.5% apart on the RAM: each is refused.
	_, err := findFamilyRates(memoryOptimizedUSEast4SKUs(41200000, 5759200), "m1-megamem-96", "us-east4", 1)
	require.ErrorContains(t, err, "ambiguous catalog")
	_, err = findFamilyRates(memoryOptimizedUSEast4SKUs(39232200, 5887000), "m1-megamem-96", "us-east4", 1)
	require.ErrorContains(t, err, "ambiguous catalog")
}

func TestFindFamilyRates_CommitmentDuplicatesStayStrict(t *testing.T) {
	t.Parallel()
	skus := append(memoryOptimizedUSEast4SKUs(39232200, 5759200),
		catalogSKU("m-c1-core-dup", "Commitment v1: Memory-optimized Cpu in Americas for 1 Year", "CPU", "Commit1Yr", "h", 25010000, []string{"us-east4"}))
	_, err := findFamilyRates(skus, "m1-megamem-96", "us-east4", 1)
	require.ErrorContains(t, err, "ambiguous catalog")
}

func TestFindFamilyRates_ThreeOnDemandDuplicatesUseWholeSpread(t *testing.T) {
	t.Parallel()
	east4 := []string{"us-east4"}
	core := func(id string, nanos int64) *cloudbilling.Sku {
		return catalogSKU(id, "Memory-optimized Instance Core running in Virginia", "CPU", "OnDemand", "h", nanos, east4)
	}
	rest := memoryOptimizedUSEast4SKUs(39200000, 5800000)[2:]
	// 100.5 and 100 are within 1% of each other, and 101.3 is within 1% of 100.5,
	// but 100 vs 101.3 is 1.28% apart: the whole spread counts.
	_, err := findFamilyRates(append([]*cloudbilling.Sku{core("a", 100500000), core("b", 100000000), core("c", 101300000)}, rest...), "m1-megamem-96", "us-east4", 1)
	require.ErrorContains(t, err, "ambiguous catalog")
}
