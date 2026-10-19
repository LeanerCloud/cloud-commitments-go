package reporter

import (
	"strings"
	"testing"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
	"github.com/LeanerCloud/cloud-commitments-go/pkg/scorer"
	"github.com/stretchr/testify/assert"
)

func makeResult(passed []common.Recommendation, filtered []scorer.FilteredRecommendation) scorer.ScoredResult {
	return scorer.ScoredResult{Passed: passed, Filtered: filtered}
}

func TestRenderTable_ColumnCount(t *testing.T) {
	result := makeResult([]common.Recommendation{
		{Provider: "aws", AccountName: "prod", Region: "us-east-1", Service: "ec2",
			ResourceType: "m5.large", Term: "1yr", Count: 3,
			CommitmentCost: 1200.0, EstimatedSavings: 400.0, SavingsPercentage: 25.0,
			CommitmentType: common.CommitmentReservedInstance},
	}, nil)

	output := RenderTable(result)
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	// Header line
	header := lines[0]
	// Split on 2+ spaces (tabwriter aligns with spaces, not tabs after flush)
	cols := strings.Fields(header)
	assert.Equal(t, 13, len(cols), "expected 13 columns in header, got: %v", cols)
}

func TestRenderTable_Empty(t *testing.T) {
	result := makeResult(nil, nil)
	output := RenderTable(result)
	assert.NotEmpty(t, output)
	assert.Contains(t, output, "No recommendations")
}

func TestRenderTable_ContainsData(t *testing.T) {
	result := makeResult([]common.Recommendation{
		{Provider: "aws", Region: "eu-west-1", Service: "rds", ResourceType: "db.r5.large",
			Count: 2, EstimatedSavings: 300.0, SavingsPercentage: 20.0},
	}, nil)
	output := RenderTable(result)
	assert.Contains(t, output, "eu-west-1")
	assert.Contains(t, output, "db.r5.large")
}

func TestRenderExcluded_Empty(t *testing.T) {
	result := makeResult(nil, nil)
	output := RenderExcluded(result)
	assert.Empty(t, output)
}

func TestRenderExcluded_ContainsReason(t *testing.T) {
	result := makeResult(nil, []scorer.FilteredRecommendation{
		{
			Recommendation: common.Recommendation{Provider: "aws", Service: "ec2", ResourceType: "m5.large"},
			FilterReason:   "savings 3.0% below minimum 5.0%",
		},
	})
	output := RenderExcluded(result)
	assert.Contains(t, output, "savings 3.0% below minimum 5.0%")
	assert.Contains(t, output, "m5.large")
}

func TestRenderSummary_Totals(t *testing.T) {
	result := makeResult([]common.Recommendation{
		{EstimatedSavings: 300.0, CommitmentCost: 900.0},
		{EstimatedSavings: 200.0, CommitmentCost: 600.0},
	}, nil)
	output := RenderSummary(result)
	assert.Contains(t, output, "$500.00")  // total savings
	assert.Contains(t, output, "$1500.00") // total cost
}

func TestRenderSummary_WithFiltered(t *testing.T) {
	result := makeResult(
		[]common.Recommendation{{EstimatedSavings: 100.0, CommitmentCost: 500.0}},
		[]scorer.FilteredRecommendation{
			{FilterReason: "savings 2.0% below minimum 5.0%"},
			{FilterReason: "savings 1.0% below minimum 5.0%"},
		},
	)
	output := RenderSummary(result)
	assert.Contains(t, output, "Filtered: 2")
}

func TestRenderSummary_FilteredReasonsSorted(t *testing.T) {
	result := makeResult(
		[]common.Recommendation{{EstimatedSavings: 100.0, CommitmentCost: 500.0}},
		[]scorer.FilteredRecommendation{
			{FilterReason: "term 3yr not allowed"},
			{FilterReason: "savings 2.0% below minimum 5.0%"},
			{FilterReason: "count 2 below minimum 5"},
			{FilterReason: "savings 1.0% below minimum 5.0%"},
			{FilterReason: "payback 18.0 months exceeds maximum"},
		},
	)
	output := RenderSummary(result)
	assert.Contains(t, output, "Filtered: 5 recommendations (count: 1, payback: 1, savings: 2, term: 1)\n")
}

func TestRenderTable_RecurringMonthlyCost(t *testing.T) {
	recurring := 1968.81
	zero := 0.0
	rows := []common.Recommendation{
		{Provider: "aws", Service: "savings-plans-ec2instance", Term: "1yr", Count: 1, RecurringMonthlyCost: &recurring, CommitmentType: common.CommitmentSavingsPlan},
		{Provider: "aws", Service: "savings-plans-compute", Term: "1yr", Count: 1, RecurringMonthlyCost: &zero, CommitmentType: common.CommitmentSavingsPlan},
		{Provider: "aws", Service: "rds", Term: "1yr", Count: 1, CommitmentType: common.CommitmentReservedInstance},
	}
	output := RenderTable(makeResult(rows, nil))
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	assert.Contains(t, lines[0], "Recurring$/mo")

	// Est.Cost and Est.Savings are also $0.00 on these rows, so look at the
	// Recurring$/mo cell itself: fifth field from the end
	// (Recurring, Est.Savings, Savings%, Break-even, Commitment).
	recurringCell := func(line string) string {
		fields := strings.Fields(line)
		return fields[len(fields)-5]
	}
	assert.Equal(t, "$1968.81", recurringCell(lines[2]))
	assert.Equal(t, "$0.00", recurringCell(lines[3]), "a known zero recurring cost (all-upfront) renders as $0.00")
	assert.Equal(t, "-", recurringCell(lines[4]), "an unknown recurring cost renders as -, not a known $0.00")
}
