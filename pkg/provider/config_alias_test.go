package provider

import "testing"

func regionOf(c Config) string { return c.Region }

// A value built with the deprecated name must stay usable as a Config so
// external consumers keep compiling.
func TestProviderConfigAliasIsConfig(t *testing.T) {
	t.Parallel()
	old := ProviderConfig{Name: "aws", Region: "us-east-1"}
	if got := regionOf(old); got != "us-east-1" {
		t.Fatalf("alias lost fields: %q", got)
	}
}
