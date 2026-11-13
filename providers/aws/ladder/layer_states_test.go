package ladder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/providers/aws/recommendations"
	ec2svc "github.com/LeanerCloud/cloud-commitments-go/providers/aws/services/ec2"
)

// Not parallel: captures the global logger.
func TestUtilsForConvertibleRIs_WarnsOnlyOnTotalJoinMiss(t *testing.T) {
	utils := []recommendations.RIUtilization{{ReservedInstanceID: "sub-1"}}
	ri := func(id string) []ec2svc.ConvertibleRI { return []ec2svc.ConvertibleRI{{ReservedInstanceID: id}} }

	buf := captureLog(t)
	assert.Empty(t, utilsForConvertibleRIs(utils, ri("ri-1")))
	assert.Contains(t, buf.String(), "SUBSCRIPTION_ID")

	for name, c := range map[string]struct {
		u  []recommendations.RIUtilization
		ri []ec2svc.ConvertibleRI
	}{
		"match":    {utils, ri("sub-1")},
		"no utils": {nil, ri("ri-1")},
		"no ris":   {utils, nil},
	} {
		buf.Reset()
		utilsForConvertibleRIs(c.u, c.ri)
		assert.Empty(t, buf.String(), name)
	}
}
