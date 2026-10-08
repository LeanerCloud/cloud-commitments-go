package insurance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func TestAssessProductSupport_DocumentedTypes(t *testing.T) {
	for _, tc := range []struct {
		provider common.ProviderType
		typ      string
	}{
		{common.ProviderAWS, "aws/AmazonEC2"},
		{common.ProviderAWS, "aws/savingsplan/Compute"},
	} {
		got := AssessProductSupport(tc.provider, tc.typ)
		assert.Equal(t, ProductSupportSupported, got.Status, tc.typ)
		assert.Equal(t, supportedServicesURL, got.Source, tc.typ)
		assert.Contains(t, got.Evidence, "inferred from the schema example; not stated by Archera", tc.typ)
	}
}

func TestAssessProductSupport_UnknownByDefault(t *testing.T) {
	for _, tc := range []struct {
		provider common.ProviderType
		typ      string
	}{
		{common.ProviderAWS, ""},
		{common.ProviderAWS, "ri"},
		{common.ProviderAWS, "aws/AmazonDynamoDB"},
		{common.ProviderAWS, "azure/Virtual Machines"}, // wrong provider for the type
		{common.ProviderGCP, "aws/AmazonEC2"},
		{"", "aws/AmazonEC2"},
		{common.ProviderAWS, "aws/amazonec2"}, // exact match only
		{common.ProviderAWS, "aws/AmazonEC2X"},
		{common.ProviderAWS, "aws/savingsplan/ComputeAnything"},
		{common.ProviderAzure, "azure/Virtual Machines"}, // ambiguous page rows: deliberately unknown
	} {
		got := AssessProductSupport(tc.provider, tc.typ)
		assert.Equal(t, ProductSupport{Status: ProductSupportUnknown}, got, "%s %q", tc.provider, tc.typ)
	}
}

func TestSupportTable_EveryRowIsCited(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range supportTable {
		key := string(r.provider) + "|" + r.commitmentType
		assert.False(t, seen[key], "duplicate row %s", key)
		seen[key] = true
		assert.NotEqual(t, ProductSupportUnknown, r.status, key)
		assert.Contains(t, r.source, "https://docs.archera.ai/", key)
		assert.Contains(t, r.evidence, "inferred from the schema example", key)
	}
}
