package aws

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	sptypes "github.com/aws/aws-sdk-go-v2/service/savingsplans/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/provider"
)

// Only the EC2 and Redshift purchase APIs lack an idempotency token. A silent Found from any
// other client would be wrong, and a wrapper hiding the reconciler would degrade to Unknown.
func TestServiceClients_PurchaseReconcilerWiring(t *testing.T) {
	t.Parallel()
	cfg := aws.Config{Region: "us-east-1"}

	constructors := map[string]struct {
		client provider.ServiceClient
		want   bool
	}{
		"ec2":          {NewEC2Client(cfg), true},
		"redshift":     {NewRedshiftClient(cfg), true},
		"rds":          {NewRDSClient(cfg), false},
		"elasticache":  {NewElastiCacheClient(cfg), false},
		"memorydb":     {NewMemoryDBClient(cfg), false},
		"opensearch":   {NewOpenSearchClient(cfg), false},
		"savingsplans": {NewSavingsPlansClient(cfg, sptypes.SavingsPlanTypeCompute), false},
	}
	for name, tc := range constructors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, ok := tc.client.(provider.PurchaseReconciler)
			assert.Equal(t, tc.want, ok)
		})
	}
}

func TestProvider_GetServiceClient_PurchaseReconciler(t *testing.T) {
	t.Parallel()
	p := &Provider{cfg: aws.Config{Region: "us-east-1"}}

	cases := map[common.ServiceType]bool{
		common.ServiceEC2:                 true,
		common.ServiceCompute:             true,
		common.ServiceRedshift:            true,
		common.ServiceDataWarehouse:       true,
		common.ServiceRDS:                 false,
		common.ServiceElastiCache:         false,
		common.ServiceMemoryDB:            false,
		common.ServiceOpenSearch:          false,
		common.ServiceSavingsPlansCompute: false,
	}
	for service, want := range cases {
		t.Run(string(service), func(t *testing.T) {
			t.Parallel()
			client, err := p.GetServiceClient(context.Background(), service, "us-east-1")
			require.NoError(t, err)
			_, ok := client.(provider.PurchaseReconciler)
			assert.Equal(t, want, ok)
		})
	}
}
