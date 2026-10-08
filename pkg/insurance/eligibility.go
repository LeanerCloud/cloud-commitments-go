package insurance

import "github.com/LeanerCloud/cloud-commitments-go/pkg/common"

// ProductSupportStatus is whether Archera's public documentation states that a
// commitment type is offered as a Guaranteed Commitment. It says nothing about
// underwriting allowances or a customer's eligibility: the API exposes neither,
// and a "supported" product can still be capped or refused. Consumers render
// allowance and customer eligibility as unknown in every case.
type ProductSupportStatus string

const (
	// ProductSupportSupported means the documentation lists guaranteed terms
	// for the type.
	ProductSupportSupported ProductSupportStatus = "supported"
	// ProductSupportUnknown means the documentation was not matched to this
	// type, which is the default.
	ProductSupportUnknown ProductSupportStatus = "unknown"
)

// ProductSupport is the documentation-derived verdict for one commitment type.
type ProductSupport struct {
	Status ProductSupportStatus
	// Source is the documentation URL a non-unknown verdict rests on.
	Source string
	// Evidence is the statement quoted from Source.
	Evidence string
}

const supportedServicesURL = "https://docs.archera.ai/help-center/getting-started/supported-reservable-services"

type supportRow struct {
	provider       common.ProviderType
	commitmentType string
	status         ProductSupportStatus
	source         string
	evidence       string
}

// supportTable holds documentation-derived verdicts. Each page cell is quoted
// verbatim, but the pages name services, not API commitment_type strings; the
// key-to-service mapping of every row is inferred from the examples in the
// OfferComparisonEntry schema and said so in the row's Evidence. A type with no
// row is unknown: do not extend this from marketing lists or by analogy, and
// add a row only with a quoted cell from a page a reviewer can open.
// Page: Supported Reservable Services, "Last verified: 17 September 2026".
var supportTable = []supportRow{
	{common.ProviderAWS, "aws/AmazonEC2", ProductSupportSupported, supportedServicesURL,
		`AWS Reserved Instances table, service "EC2": Guaranteed terms "30 day, 1 year". ` +
			`Mapping of API type "aws/AmazonEC2" to page service "EC2" is inferred from the schema example; not stated by Archera.`},
	{common.ProviderAWS, "aws/savingsplan/Compute", ProductSupportSupported, supportedServicesURL,
		`AWS Savings Plans table, "Compute Savings Plan": Guaranteed terms "30 day, 1 year". ` +
			`Mapping of API type "aws/savingsplan/Compute" to page service "Compute Savings Plan" is inferred from the schema example; not stated by Archera.`},
}

// AssessProductSupport looks up the documented support verdict for a
// provider and vendor commitment_type. Anything not in the table, including
// every unrecognized type string, is ProductSupportUnknown.
func AssessProductSupport(provider common.ProviderType, commitmentType string) ProductSupport {
	for _, r := range supportTable {
		if r.provider == provider && r.commitmentType == commitmentType {
			return ProductSupport{Status: r.status, Source: r.source, Evidence: r.evidence}
		}
	}
	return ProductSupport{Status: ProductSupportUnknown}
}
