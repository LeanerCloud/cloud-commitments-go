package computeengine

import (
	"fmt"
	"math"

	"cloud.google.com/go/compute/apiv1/computepb"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func commitmentVCPUCount(resources []*computepb.ResourceCommitment) (int, error) {
	var count int
	seen := make(map[string]bool)
	for _, resource := range resources {
		kind := resource.GetType()
		value, ok := computepb.ResourceCommitment_Type_value[kind]
		if !ok || value == int32(computepb.ResourceCommitment_UNDEFINED_TYPE) {
			return 0, fmt.Errorf("unknown commitment resource type %q", kind)
		}
		if seen[kind] {
			return 0, fmt.Errorf("duplicate commitment resource type %q", kind)
		}
		seen[kind] = true
		if resource.Amount == nil || resource.GetAmount() <= 0 {
			return 0, fmt.Errorf("commitment resource %q requires an explicit positive amount", kind)
		}
		if kind == computepb.ResourceCommitment_VCPU.String() {
			if resource.GetAmount() > math.MaxInt {
				return 0, fmt.Errorf("VCPU amount overflows int")
			}
			count = int(resource.GetAmount())
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("commitment has no VCPU resource; cannot determine duplicate-purchase quantity")
	}
	return count, nil
}

type commitmentPool struct {
	account string
	region  string
	family  string
}

// FilterRecommendationsForRecentCommitments defers the whole family: scalar vCPU
// subtraction cannot safely resize the independently committed memory amount.
func (c *Client) FilterRecommendationsForRecentCommitments(recs []common.Recommendation, existing []common.Commitment) (passed, filtered []common.Recommendation, err error) {
	pools := make(map[commitmentPool]bool)
	for i := range existing {
		commitment := &existing[i]
		if err := validateCUDIdentity(commitment.Provider, commitment.Service, commitment.CommitmentType, commitment.Account, commitment.Region); err != nil {
			return nil, nil, err
		}
		pools[commitmentPool{commitment.Account, commitment.Region, commitment.ResourceType}] = true
	}
	for i := range recs {
		rec := &recs[i]
		if err := validateCUDIdentity(rec.Provider, rec.Service, rec.CommitmentType, rec.Account, rec.Region); err != nil {
			return nil, nil, err
		}
		family, err := commitmentTypeForMachineType(rec.ResourceType)
		if err != nil {
			return nil, nil, err
		}
		if pools[commitmentPool{rec.Account, rec.Region, family.String()}] {
			filtered = append(filtered, *rec)
		} else {
			passed = append(passed, *rec)
		}
	}
	return passed, filtered, nil
}

func validateCUDIdentity(provider common.ProviderType, service common.ServiceType, kind common.CommitmentType, account, region string) error {
	if provider != common.ProviderGCP || service != common.ServiceCompute || kind != common.CommitmentCUD || account == "" || region == "" {
		return fmt.Errorf("computeengine: invalid CUD identity: provider=%q service=%q kind=%q account=%q region=%q", provider, service, kind, account, region)
	}
	return nil
}
