package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

import "strings"

// ec2NormalizationFactors maps EC2 instance sizes to their AWS normalization factors.
// "metal" is deliberately absent: its factor equals the family's largest size
// (i3.metal 128, m5.metal 192, m5zn.metal 96), and some families have no metal
// size, so it cannot be a family-independent constant. Callers that know it set
// RIInfo.NormalizationFactor.
// See: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/apply_ri.html
var ec2NormalizationFactors = map[string]float64{
	"nano":      0.25,
	"micro":     0.5,
	"small":     1,
	"medium":    2,
	"large":     4,
	"xlarge":    8,
	"2xlarge":   16,
	"3xlarge":   24,
	"4xlarge":   32,
	"6xlarge":   48,
	"8xlarge":   64,
	"9xlarge":   72,
	"10xlarge":  80,
	"12xlarge":  96,
	"16xlarge":  128,
	"18xlarge":  144,
	"24xlarge":  192,
	"32xlarge":  256,
	"48xlarge":  384,
	"56xlarge":  448,
	"96xlarge":  768,
	"112xlarge": 896,
}

// EC2NormalizationFactor returns the AWS normalization factor of an EC2 instance size such
// as "xlarge" (8). It returns 0 when the size is not recognized, including "metal".
func EC2NormalizationFactor(size string) float64 {
	return ec2NormalizationFactors[size]
}

// ec2NotSizeFlexibleFamilies are the families whose regional Linux/Unix reservations are
// still not size-flexible ("Instance size flexibility > Limitations"):
// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/apply_ri.html#ri-instance-size-flexibility-limitations
// Re-read that list when AWS adds a family: a missing entry lets the duplicate guard
// credit a reservation to sizes it does not cover.
var ec2NotSizeFlexibleFamilies = map[string]bool{
	"g4ad": true, "g4dn": true, "g5": true, "g5g": true, "g6": true, "g6e": true, "g6f": true,
	"gr6": true, "gr6f": true, "hpc7a": true, "p5": true, "inf1": true, "inf2": true,
	"u7i-6tb": true, "u7i-8tb": true,
}

// EC2SizeFlexibleFamily reports whether a regional Linux/Unix default-tenancy reservation of
// the instance family (the part of the type before the dot, such as "m5" or "u7i-6tb") can be
// size-flexible. It is case-insensitive and false for the families AWS excludes.
func EC2SizeFlexibleFamily(family string) bool {
	return !ec2NotSizeFlexibleFamilies[strings.ToLower(strings.TrimSpace(family))]
}
