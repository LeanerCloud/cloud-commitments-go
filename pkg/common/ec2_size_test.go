package common //nolint:revive // see ec2_size.go

import "testing"

func TestEC2NormalizationFactor(t *testing.T) {
	t.Parallel()
	for size, want := range map[string]float64{"nano": 0.25, "large": 4, "xlarge": 8, "24xlarge": 192, "metal": 0, "bogus": 0, "": 0} {
		if got := EC2NormalizationFactor(size); got != want {
			t.Errorf("EC2NormalizationFactor(%q) = %v, want %v", size, got, want)
		}
	}
}
