package common //nolint:revive // see ec2_size.go

import "testing"

func TestEC2NormalizationFactor(t *testing.T) {
	t.Parallel()
	for size, want := range map[string]float64{"nano": 0.25, "large": 4, "xlarge": 8, "24xlarge": 192, "32xlarge": 256, "48xlarge": 384, "56xlarge": 448, "96xlarge": 768, "112xlarge": 896, "metal": 0, "bogus": 0, "": 0} {
		if got := EC2NormalizationFactor(size); got != want {
			t.Errorf("EC2NormalizationFactor(%q) = %v, want %v", size, got, want)
		}
	}
}

func TestEC2SizeFlexibleFamily(t *testing.T) {
	t.Parallel()
	for family, want := range map[string]bool{
		"m5": true, "c5n": true, "t3": true, "g5": false, "G5": false, "g4dn": false, "g4ad": false, "g5g": false,
		"gr6f": false, "p5": false, "p4d": true, "inf1": false, "inf2": false, "hpc7a": false, "hpc7g": true,
		"u7i-6tb": false, "u7i-8tb": false, "u7i-12tb": true, "g6e": false, "g4": true,
	} {
		if got := EC2SizeFlexibleFamily(family); got != want {
			t.Errorf("EC2SizeFlexibleFamily(%q) = %v, want %v", family, got, want)
		}
	}
}
