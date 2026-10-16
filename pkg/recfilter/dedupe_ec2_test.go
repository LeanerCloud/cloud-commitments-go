package recfilter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopeCovers(t *testing.T) {
	t.Parallel()
	regional, unknown := scopeState{kind: scopeRegionalKind}, scopeState{}
	zonalA, zonalB := scopeState{kind: scopeZonalKind, az: "us-east-1a"}, scopeState{kind: scopeZonalKind, az: "us-east-1b"}
	zonalAny := scopeState{kind: scopeZonalKind}
	tests := []struct {
		name       string
		entry, rec scopeState
		want       bool
	}{
		{"regional covers regional", regional, regional, true},
		{"regional covers zonal", regional, zonalA, true},
		{"regional covers unknown", regional, unknown, true},
		{"zonal a covers zonal a", zonalA, zonalA, true},
		{"zonal a covers unknown", zonalA, unknown, true},
		{"zonal a not zonal b", zonalA, zonalB, false},
		{"zonal a not regional", zonalA, regional, false},
		{"zonal no az covers zonal a", zonalAny, zonalA, true},
		{"zonal no az covers unknown", zonalAny, unknown, true},
		{"zonal no az not regional", zonalAny, regional, false},
		{"unknown covers regional", unknown, regional, true},
		{"unknown covers zonal", unknown, zonalA, true},
		{"unknown covers unknown", unknown, unknown, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, scopeCovers(tt.entry, tt.rec))
		})
	}
	assert.Equal(t, zonalA, newScopeState("", " US-East-1A "))
	assert.Equal(t, unknown, newScopeState("bogus", ""))
}

func ec2Commitment(platform, tenancy, scope, az string, count int) common.Commitment {
	return common.Commitment{Provider: common.ProviderAWS, Service: common.ServiceEC2, ResourceType: "m5.large",
		Region: "us-east-1", Platform: platform, Tenancy: tenancy, Scope: scope, AvailabilityZone: az, Count: count,
		State: common.CommitmentStateActive, StartDate: time.Now(), Account: "acct-a"}
}

func ec2Rec(platform, tenancy, scope, az string, count int) common.Recommendation {
	monthly := 10.0 * float64(count)
	return common.Recommendation{Provider: common.ProviderAWS, Service: common.ServiceCompute, ResourceType: "m5.large",
		Region: "us-east-1", Count: count, EstimatedSavings: monthly, RecurringMonthlyCost: &monthly, Account: "acct-b",
		Details: &common.ComputeDetails{InstanceType: "m5.large", Platform: platform, Tenancy: tenancy, Scope: scope, AvailabilityZone: az}}
}

func runEC2(t *testing.T, logf Logf, commitments []common.Commitment, recs ...common.Recommendation) []common.Recommendation {
	t.Helper()
	checker := NewDuplicateChecker(0)
	checker.Logf = logf
	passed, _, err := checker.AdjustRecommendationsForExisting(context.Background(), recs, &fakeServiceClient{commitments: commitments})
	require.NoError(t, err)
	return passed
}

func TestEC2DedupeMatching(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		commitment common.Commitment
		rec        common.Recommendation
		suppressed bool
	}{
		{"windows RI does not cover linux", ec2Commitment("Windows", "default", "regional", "", 1), ec2Rec("Linux/UNIX", "default", "regional", "", 1), false},
		{"dedicated does not cover default", ec2Commitment("linux", "dedicated", "regional", "", 1), ec2Rec("linux", "default", "regional", "", 1), false},
		{"zonal RI does not cover regional rec", ec2Commitment("linux", "default", "zonal", "us-east-1a", 1), ec2Rec("linux", "default", "regional", "", 1), false},
		{"zonal RI does not cover other zone", ec2Commitment("linux", "default", "zonal", "us-east-1a", 1), ec2Rec("linux", "default", "zonal", "us-east-1b", 1), false},
		{"regional RI covers zonal rec", ec2Commitment("linux", "default", "regional", "", 1), ec2Rec("linux", "default", "zonal", "us-east-1a", 1), true},
		{"zonal RI covers same zone, case-insensitive", ec2Commitment("linux", "default", "zonal", "US-EAST-1A", 1), ec2Rec("linux", "default", "zonal", "us-east-1a", 1), true},
		{"amazon vpc suffix", ec2Commitment("Linux/UNIX (Amazon VPC)", "default", "regional", "", 1), ec2Rec("Linux/UNIX", "default", "regional", "", 1), true},
		{"shared is default", ec2Commitment("linux", "shared", "regional", "", 1), ec2Rec("linux", "default", "regional", "", 1), true},
		{"Region is regional", ec2Commitment("linux", "default", "Region", "", 1), ec2Rec("linux", "default", "regional", "", 1), true},
		{"commitment platform wildcard", ec2Commitment("", "default", "regional", "", 1), ec2Rec("windows", "default", "regional", "", 1), true},
		{"rec platform wildcard", ec2Commitment("windows", "default", "regional", "", 1), ec2Rec("", "default", "regional", "", 1), true},
		{"commitment tenancy wildcard", ec2Commitment("linux", "", "regional", "", 1), ec2Rec("linux", "dedicated", "regional", "", 1), true},
		{"rec tenancy wildcard", ec2Commitment("linux", "dedicated", "regional", "", 1), ec2Rec("linux", "", "regional", "", 1), true},
		{"commitment scope wildcard", ec2Commitment("linux", "default", "", "", 1), ec2Rec("linux", "default", "regional", "", 1), true},
		{"rec scope wildcard", ec2Commitment("linux", "default", "zonal", "us-east-1a", 1), ec2Rec("linux", "default", "", "", 1), true},
		{"different account still suppresses", ec2Commitment("linux", "default", "regional", "", 1), ec2Rec("linux", "default", "regional", "", 1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			passed := runEC2(t, nil, []common.Commitment{tt.commitment}, tt.rec)
			assert.Equal(t, !tt.suppressed, len(passed) == 1)
		})
	}
}

func TestEC2DedupeValueDetails(t *testing.T) {
	t.Parallel()
	rec := ec2Rec("windows", "default", "regional", "", 1)
	rec.Details = *rec.Details.(*common.ComputeDetails)
	assert.Empty(t, runEC2(t, nil, []common.Commitment{ec2Commitment("windows", "default", "regional", "", 1)}, rec))
	var typedNil *common.ComputeDetails
	rec.Details = typedNil
	assert.Empty(t, runEC2(t, nil, []common.Commitment{ec2Commitment("windows", "default", "regional", "", 1)}, rec))
}

func TestEC2DedupeAzureNotKeyed(t *testing.T) {
	t.Parallel()
	c := common.Commitment{Provider: common.ProviderAzure, Service: common.ServiceCompute, ResourceType: "Standard_D2s_v5",
		Region: "eastus", Platform: "windows", Count: 1, State: common.CommitmentStateActive, StartDate: time.Now()}
	rec := common.Recommendation{Provider: common.ProviderAzure, Service: common.ServiceCompute, ResourceType: "Standard_D2s_v5",
		Region: "eastus", Count: 1, Details: &common.ComputeDetails{Platform: "linux"}}
	assert.Empty(t, runEC2(t, nil, []common.Commitment{c}, rec))
}

func TestEC2DedupeWildcardPreference(t *testing.T) {
	t.Parallel()
	commitments := []common.Commitment{
		ec2Commitment("", "", "", "", 2),
		ec2Commitment("linux", "default", "regional", "", 1),
		ec2Commitment("linux", "default", "regional", "", 1),
	}
	for i := 0; i < 5; i++ {
		passed := runEC2(t, nil, commitments, ec2Rec("linux", "default", "regional", "", 5))
		require.Len(t, passed, 1)
		assert.Equal(t, 1, passed[0].Count)
		assert.InDelta(t, 10.0, passed[0].EstimatedSavings, 1e-9)
		assert.InDelta(t, 10.0, *passed[0].RecurringMonthlyCost, 1e-9)
	}
	passed := runEC2(t, nil, commitments, ec2Rec("windows", "default", "regional", "", 1), ec2Rec("windows", "default", "regional", "", 1), ec2Rec("windows", "default", "regional", "", 1))
	assert.Len(t, passed, 1)
}

func TestEC2DedupeEntryOrder(t *testing.T) {
	t.Parallel()
	// Fewest wildcards first: linux takes the exact entry, leaving the
	// wildcard platform entry for windows. Reversed, windows would pass.
	passed := runEC2(t, nil, []common.Commitment{
		ec2Commitment("", "default", "regional", "", 1),
		ec2Commitment("linux", "default", "regional", "", 1),
	}, ec2Rec("linux", "default", "regional", "", 1), ec2Rec("windows", "default", "regional", "", 1))
	assert.Empty(t, passed)

	// Equal wildcard counts keep input order: the first entry (zonal a) is
	// consumed by the first rec, leaving only zone b for the second.
	passed = runEC2(t, nil, []common.Commitment{
		ec2Commitment("linux", "default", "zonal", "us-east-1a", 1),
		ec2Commitment("linux", "default", "zonal", "us-east-1b", 1),
	}, ec2Rec("linux", "default", "", "", 1), ec2Rec("linux", "default", "zonal", "us-east-1a", 1))
	require.Len(t, passed, 1)
	assert.Equal(t, "us-east-1a", passed[0].Details.(*common.ComputeDetails).AvailabilityZone)
}

func TestEC2DedupeNoWarnForSQLServerPlatform(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	runEC2(t, logf, []common.Commitment{ec2Commitment("Linux with SQL Server Standard", "default", "regional", "", 1)},
		ec2Rec("Linux with SQL Server Standard", "default", "regional", "", 1))
	for _, l := range lines {
		assert.NotContains(t, l, "WARNING")
	}
}

func TestEC2DedupeWarnsOncePerUnrecognizedValue(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	runEC2(t, logf, []common.Commitment{ec2Commitment("linux", "default", "weird-scope", "", 1)},
		ec2Rec("beos", "weird-tenancy", "weird-scope", "", 1), ec2Rec("linux", "weird-tenancy", "weird-scope", "", 1))
	var warnings []string
	for _, l := range lines {
		if strings.Contains(l, "WARNING") {
			warnings = append(warnings, l)
		}
	}
	require.Len(t, warnings, 3)
	assert.Contains(t, warnings[0], `scope "weird-scope"`)
	assert.Contains(t, warnings[0], "treating it as a wildcard")
	assert.Contains(t, warnings[1], `platform "beos"`)
	assert.Contains(t, warnings[1], "matching it only against the same platform")
	assert.Contains(t, warnings[2], `tenancy "weird-tenancy"`)
}
