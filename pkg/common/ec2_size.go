package common //nolint:revive // pkg/common is the module's core domain package, imported by name (common.X) throughout this repo and by consumer-mcp

// ec2NormalizationFactors maps EC2 instance sizes to their AWS normalization factors.
// "metal" is deliberately absent: its factor equals the family's largest size
// (i3.metal 128, m5.metal 192, m5zn.metal 96), and some families have no metal
// size, so it cannot be a family-independent constant. Callers that know it set
// RIInfo.NormalizationFactor.
// See: https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ri-modification-instancemove.html
var ec2NormalizationFactors = map[string]float64{
	"nano":     0.25,
	"micro":    0.5,
	"small":    1,
	"medium":   2,
	"large":    4,
	"xlarge":   8,
	"2xlarge":  16,
	"3xlarge":  24,
	"4xlarge":  32,
	"6xlarge":  48,
	"8xlarge":  64,
	"9xlarge":  72,
	"10xlarge": 80,
	"12xlarge": 96,
	"16xlarge": 128,
	"18xlarge": 144,
	"24xlarge": 192,
}

// EC2NormalizationFactor returns the AWS normalization factor of an EC2 instance size such
// as "xlarge" (8). It returns 0 when the size is not recognized, including "metal".
func EC2NormalizationFactor(size string) float64 {
	return ec2NormalizationFactors[size]
}
