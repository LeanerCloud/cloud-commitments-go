package computeengine

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"google.golang.org/api/cloudbilling/v1"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/providers/gcp/internal/skumatch"
)

const (
	// computeServiceID is the Cloud Billing catalog service for Compute Engine.
	computeServiceID = "services/6F81-5844-456A"

	usageUnitHour    = "h"
	usageUnitGiBHour = "GiBy.h"

	usageTypeCommit1Yr = "Commit1Yr"
	usageTypeCommit3Yr = "Commit3Yr"
)

// ComputePricing is the price of a commitment's resources, for one term.
type ComputePricing struct {
	// HourlyRate is the committed price per hour for the whole resource set.
	HourlyRate float64
	// CommitmentPrice is the committed price over the whole term.
	CommitmentPrice float64
	// OnDemandPrice is the on-demand price of the same resources over the term.
	OnDemandPrice     float64
	Currency          string
	SavingsPercentage float64
}

// computeResources is the amount a commitment covers.
type computeResources struct {
	vcpus     int
	memoryGiB float64
}

// resourcesFromRecommendation reads the vCPU count and memory a recommendation
// covers; both are required to price it.
func resourcesFromRecommendation(rec common.Recommendation) (computeResources, error) {
	if rec.Count <= 0 {
		return computeResources{}, fmt.Errorf("recommendation for %s has no vCPU count; cannot price it", rec.ResourceType)
	}
	memMB, err := memoryMBFromDetails(rec)
	if err != nil {
		return computeResources{}, err
	}
	return computeResources{vcpus: rec.Count, memoryGiB: float64(memMB) / 1024.0}, nil
}

// skuFamilyLabels holds how the catalog names a machine family. The labels are
// not derivable from the machine type prefix for every family.
type skuFamilyLabels struct {
	onDemand string   // "<label> Instance Core running in ..."
	commit   []string // "Commitment v1: <label> Cpu in ..."
	// legacyCommit lists labels the catalog also names "Commitment: <label> Core
	// running in ..." (no "v1:", Core/Ram instead of Cpu/Ram). An allowlist, not a
	// pattern: a family absent here never matches the legacy form.
	legacyCommit []string
}

// skuFamilyOverrides lists families whose catalog label differs from the
// upper-cased machine type prefix. Verified against the live catalog.
var skuFamilyOverrides = map[string]skuFamilyLabels{
	"n1":  {onDemand: "N1", commit: []string{"N1", ""}}, // older regions omit the family
	"n2d": {onDemand: "N2D AMD", commit: []string{"N2D AMD"}},
	"c2d": {onDemand: "C2D AMD", commit: []string{"C2D AMD"}},
	"t2d": {onDemand: "T2D AMD", commit: []string{"T2D AMD"}},
	"c4a": {onDemand: "C4A Arm", commit: []string{"C4A Arm"}},
	"c2":  {onDemand: "Compute optimized", commit: []string{"Compute optimized"}, legacyCommit: []string{"Compute optimized"}},
	"m1":  {onDemand: "Memory-optimized", commit: []string{"Memory-optimized"}},
	"m2":  {onDemand: "Memory-optimized", commit: []string{"Memory-optimized"}},
	"m3":  {onDemand: "M3 Memory-optimized", commit: []string{"M3 Memory-optimized"}},
}

func familyLabels(machineType string) (skuFamilyLabels, error) {
	family, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(machineType)), "-")
	if family == "" {
		return skuFamilyLabels{}, fmt.Errorf("no machine family in machine type %q", machineType)
	}
	if labels, ok := skuFamilyOverrides[family]; ok {
		return labels, nil
	}
	label := strings.ToUpper(family)
	return skuFamilyLabels{onDemand: label, commit: []string{label}}, nil
}

// skuRates is the per-vCPU-hour and per-GiB-hour price of one family.
type skuRates struct {
	cpu, ram float64
}

type familyRates struct {
	onDemand, commitment skuRates
	currency             string
}

// onDemandDescription matches e.g. "N2 Instance Core running in Americas",
// "N1 Predefined Instance Ram running in Americas" and "Compute optimized Core
// running in Americas". Sole-tenancy, custom, DWS, reserved and spot SKUs carry
// extra words in front of the family and so do not match.
func onDemandDescription(label string) *regexp.Regexp {
	return regexp.MustCompile(`^` + regexp.QuoteMeta(label) + `(?: Predefined)?(?: Instance)? (Core|Ram) running in `)
}

// commitmentDescription matches e.g. "Commitment v1: N2 Cpu in Americas for 1 Year".
func commitmentDescription(label string) *regexp.Regexp {
	family := regexp.QuoteMeta(label) + " "
	if label == "" {
		family = ""
	}
	return regexp.MustCompile(`^Commitment v1: ` + family + `(Cpu|Ram) in .* for (1|3) Years?$`)
}

// legacyCommitmentDescription matches e.g. "Commitment: Compute optimized Core
// running in Americas for 1 Year". Group 1 is Core|Ram and group 2 the term, the
// same positions as commitmentDescription.
func legacyCommitmentDescription(label string) *regexp.Regexp {
	return regexp.MustCompile(`^Commitment: ` + regexp.QuoteMeta(label) + ` (Core|Ram) running in .* for (1|3) Years?$`)
}

// rateMatcher recognizes the catalog SKUs of one family, region and term.
type rateMatcher struct {
	region      string
	onDemand    *regexp.Regexp
	commitments []*regexp.Regexp
	wantTerm    string
	wantUsage   string
}

func newRateMatcher(labels skuFamilyLabels, region string, termYears int) rateMatcher {
	m := rateMatcher{region: region, onDemand: onDemandDescription(labels.onDemand), wantTerm: "1", wantUsage: usageTypeCommit1Yr}
	if termYears == 3 {
		m.wantTerm, m.wantUsage = "3", usageTypeCommit3Yr
	}
	for _, label := range labels.commit {
		m.commitments = append(m.commitments, commitmentDescription(label))
	}
	for _, label := range labels.legacyCommit {
		m.commitments = append(m.commitments, legacyCommitmentDescription(label))
	}
	return m
}

// classify says whether the SKU is one of the four prices wanted, and which.
// matched is false for every other SKU in the catalog.
func (m rateMatcher) classify(sku *cloudbilling.Sku) (isCommit, isRAM, matched bool, err error) {
	if sku.Category == nil || sku.Category.ResourceFamily != "Compute" || !skumatch.InRegion(sku, m.region) {
		return false, false, false, nil
	}
	if sku.Category.UsageType == "OnDemand" {
		sub := m.onDemand.FindStringSubmatch(sku.Description)
		if sub == nil {
			return false, false, false, nil
		}
		return false, sub[1] == "Ram", true, nil
	}
	for _, re := range m.commitments {
		sub := re.FindStringSubmatch(sku.Description)
		if len(sub) == 0 || sub[2] != m.wantTerm {
			continue
		}
		if sku.Category.UsageType != m.wantUsage {
			return false, false, false, fmt.Errorf("SKU %s (%q) has usage type %q, expected %q", sku.SkuId, sku.Description, sku.Category.UsageType, m.wantUsage)
		}
		return true, sub[1] == "Ram", true, nil
	}
	return false, false, false, nil
}

// rateSlot is one price being collected.
type rateSlot struct {
	value float64
	seen  bool
}

func (r *rateSlot) set(price float64, sku *cloudbilling.Sku) error {
	if r.seen && r.value != price {
		return fmt.Errorf("ambiguous catalog: SKU %s (%q) prices %v but another matching SKU prices %v", sku.SkuId, sku.Description, price, r.value)
	}
	r.value, r.seen = price, true
	return nil
}

// rateCollector accumulates the four prices wanted from matched SKUs.
type rateCollector struct {
	odCPU, odRAM, cCPU, cRAM rateSlot
	currency                 string
}

func (c *rateCollector) add(sku *cloudbilling.Sku, isCommit, isRAM bool) error {
	price, currency, unit, err := skuUnitPrice(sku)
	if err != nil {
		return err
	}
	wantUnit := usageUnitHour
	if isRAM {
		wantUnit = usageUnitGiBHour
	}
	if unit != wantUnit {
		return fmt.Errorf("SKU %s (%q) is priced per %q, expected %q", sku.SkuId, sku.Description, unit, wantUnit)
	}
	if c.currency != "" && c.currency != currency {
		return fmt.Errorf("SKU %s (%q) is in %s but other matching SKUs are in %s", sku.SkuId, sku.Description, currency, c.currency)
	}
	c.currency = currency

	slot := &c.odCPU
	switch {
	case isCommit && isRAM:
		slot = &c.cRAM
	case isCommit:
		slot = &c.cCPU
	case isRAM:
		slot = &c.odRAM
	}
	return slot.set(price, sku)
}

func (c *rateCollector) rates(machineType, region string, termYears int) (*familyRates, error) {
	switch {
	case !c.odCPU.seen || !c.odRAM.seen:
		return nil, fmt.Errorf("no on-demand core and RAM pricing found for machine family of %s in %s", machineType, region)
	case !c.cCPU.seen || !c.cRAM.seen:
		return nil, fmt.Errorf("no commitment pricing found for machine type %s in region %s: catalog has no %d-year CUD core and RAM SKU for the family; cannot compute savings percentage", machineType, region, termYears)
	}
	return &familyRates{
		onDemand:   skuRates{cpu: c.odCPU.value, ram: c.odRAM.value},
		commitment: skuRates{cpu: c.cCPU.value, ram: c.cRAM.value},
		currency:   c.currency,
	}, nil
}

// findFamilyRates selects the on-demand and commitment CPU and RAM rates for the
// machine family, region and term out of the Compute Engine catalog.
func findFamilyRates(skus []*cloudbilling.Sku, machineType, region string, termYears int) (*familyRates, error) {
	labels, err := familyLabels(machineType)
	if err != nil {
		return nil, err
	}
	matcher := newRateMatcher(labels, region, termYears)

	var collector rateCollector
	for _, sku := range skus {
		isCommit, isRAM, matched, err := matcher.classify(sku)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		if err := collector.add(sku, isCommit, isRAM); err != nil {
			return nil, err
		}
	}
	return collector.rates(machineType, region, termYears)
}

// skuUnitPrice returns the first tier's unit price, currency and usage unit.
func skuUnitPrice(sku *cloudbilling.Sku) (price float64, currency, unit string, err error) {
	if len(sku.PricingInfo) == 0 || sku.PricingInfo[0].PricingExpression == nil {
		return 0, "", "", fmt.Errorf("SKU %s (%q) has no pricing", sku.SkuId, sku.Description)
	}
	expr := sku.PricingInfo[0].PricingExpression
	if len(expr.TieredRates) == 0 || expr.TieredRates[0].UnitPrice == nil {
		return 0, "", "", fmt.Errorf("SKU %s (%q) has no unit price", sku.SkuId, sku.Description)
	}
	money := expr.TieredRates[0].UnitPrice
	price = float64(money.Units) + float64(money.Nanos)/1e9
	if price <= 0 {
		return 0, "", "", fmt.Errorf("SKU %s (%q) has non-positive price %v", sku.SkuId, sku.Description, price)
	}
	if money.CurrencyCode == "" {
		return 0, "", "", fmt.Errorf("SKU %s (%q) has no currency", sku.SkuId, sku.Description)
	}
	return price, money.CurrencyCode, expr.UsageUnit, nil
}

// getComputePricing prices a commitment for res over the term from the Cloud
// Billing catalog: vCPUs times the family core rate plus GiB times the family RAM
// rate, for both the commitment and on demand. It errors when the catalog has no
// commitment price rather than fabricating one.
func (c *Client) getComputePricing(ctx context.Context, machineType, region string, termYears int, res computeResources) (*ComputePricing, error) {
	skus, err := c.listComputeSKUs(ctx)
	if err != nil {
		return nil, err
	}
	rates, err := findFamilyRates(skus, machineType, region, termYears)
	if err != nil {
		return nil, err
	}

	hours := common.HoursPerYear * float64(termYears)
	commitHourly := float64(res.vcpus)*rates.commitment.cpu + res.memoryGiB*rates.commitment.ram
	onDemandHourly := float64(res.vcpus)*rates.onDemand.cpu + res.memoryGiB*rates.onDemand.ram

	return &ComputePricing{
		HourlyRate:        commitHourly,
		CommitmentPrice:   commitHourly * hours,
		OnDemandPrice:     onDemandHourly * hours,
		Currency:          rates.currency,
		SavingsPercentage: (onDemandHourly - commitHourly) / onDemandHourly * 100,
	}, nil
}

// listComputeSKUs returns the Compute Engine catalog, fetched once per client:
// it is 33k SKUs over several pages and every recommendation prices against it.
func (c *Client) listComputeSKUs(ctx context.Context) ([]*cloudbilling.Sku, error) {
	c.skuMu.Lock()
	defer c.skuMu.Unlock()
	if c.skuCache != nil {
		return c.skuCache, nil
	}
	svc, err := c.getOrCreateBillingService(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := svc.ListSKUs(computeServiceID)
	if err != nil {
		return nil, fmt.Errorf("failed to list SKUs: %w", err)
	}
	c.skuCache = resp.Skus
	return c.skuCache, nil
}
