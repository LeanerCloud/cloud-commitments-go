package recommendations

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestARMRegion(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"eastus":    "eastus",
		"East US":   "eastus",
		"EastUS":    "eastus",
		" east us ": "eastus",
		"East\tUS":  "eastus",
		"East US":   "eastus",
		"":          "",
	} {
		assert.Equal(t, want, ARMRegion(in), "%q", in)
	}
}
