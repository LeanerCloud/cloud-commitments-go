package computeengine

import (
	"fmt"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

func concreteCommitmentType(kind string) (computepb.Commitment_Type, error) {
	value, ok := computepb.Commitment_Type_value[kind]
	typed := computepb.Commitment_Type(value)
	if !ok || typed == computepb.Commitment_UNDEFINED_TYPE || typed == computepb.Commitment_TYPE_UNSPECIFIED {
		return computepb.Commitment_UNDEFINED_TYPE, fmt.Errorf("unknown or non-concrete commitment type %q", kind)
	}
	if strings.HasPrefix(kind, "ACCELERATOR_OPTIMIZED") || strings.HasPrefix(kind, "GRAPHICS_OPTIMIZED") || strings.HasPrefix(kind, "STORAGE_OPTIMIZED") {
		return computepb.Commitment_UNDEFINED_TYPE, fmt.Errorf("commitment type %q is unsupported by this VCPU+MEMORY client", kind)
	}
	return typed, nil
}

func commitmentTypeForRecommendation(rec common.Recommendation) (computepb.Commitment_Type, error) {
	var kind string
	switch details := rec.Details.(type) {
	case common.ComputeDetails:
		kind = details.GCPCommitmentType
	case *common.ComputeDetails:
		if details != nil {
			kind = details.GCPCommitmentType
		}
	}
	if kind != "" {
		return concreteCommitmentType(kind)
	}
	return commitmentTypeForMachineType(rec.ResourceType)
}
