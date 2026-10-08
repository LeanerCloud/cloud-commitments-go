package computeengine

import (
	"fmt"
	"strings"

	"cloud.google.com/go/compute/apiv1/computepb"
	"cloud.google.com/go/recommender/apiv1/recommenderpb"
	"google.golang.org/protobuf/types/known/structpb"

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

func selectRootCommitment(content *recommenderpb.RecommendationContent) (*recommenderpb.Operation, error) {
	var root *recommenderpb.Operation
	mutations := 0
	for _, group := range content.GetOperationGroups() {
		for _, op := range group.GetOperations() {
			if op.GetResourceType() != "compute.googleapis.com/Commitment" || strings.EqualFold(op.GetAction(), "test") {
				continue
			}
			mutations++
			if op.GetPath() == "/" && (strings.EqualFold(op.GetAction(), "add") || strings.EqualFold(op.GetAction(), "replace")) {
				root = op
			}
		}
	}
	if root == nil {
		return nil, nil
	}
	if mutations != 1 {
		return nil, fmt.Errorf("unsupported compound Commitment mutations")
	}
	return root, nil
}

func rootResourceAmount(resource *structpb.Value) (string, int64, error) {
	entry := resource.GetStructValue()
	if entry == nil {
		return "", 0, fmt.Errorf("root Commitment resource must be an object")
	}
	kind := entry.Fields["type"].GetStringValue()
	if kind != computepb.ResourceCommitment_VCPU.String() && kind != computepb.ResourceCommitment_MEMORY.String() {
		return "", 0, fmt.Errorf("root Commitment resource %q is unsupported by this VCPU+MEMORY client", kind)
	}
	if kind == computepb.ResourceCommitment_VCPU.String() {
		count, err := recommendationVCPUAmount(entry.Fields["amount"])
		return kind, int64(count), err
	}
	amount, err := recommendationAmount(entry.Fields["amount"], kind)
	return kind, amount, err
}

func rootCommitmentResources(resources []*structpb.Value) (int, int64, error) {
	count, memory := 0, int64(0)
	seen := make(map[string]bool)
	for _, resource := range resources {
		kind, amount, err := rootResourceAmount(resource)
		if err != nil {
			return 0, 0, err
		}
		if seen[kind] {
			return 0, 0, fmt.Errorf("duplicate root Commitment resource %q", kind)
		}
		seen[kind] = true
		if kind == computepb.ResourceCommitment_VCPU.String() {
			count = int(amount)
		} else {
			memory = amount
		}
	}
	if count == 0 || memory == 0 {
		return 0, 0, fmt.Errorf("unsupported root Commitment shape: this client requires VCPU and MEMORY")
	}
	return count, memory, nil
}

func rootCommitmentDetails(content *recommenderpb.RecommendationContent) (int, *common.ComputeDetails, error) {
	root, err := selectRootCommitment(content)
	if err != nil {
		return 0, nil, err
	}
	if root == nil {
		return 0, nil, nil
	}
	object := root.GetValue().GetStructValue()
	if object == nil {
		return 0, nil, fmt.Errorf("root Commitment value must be an object")
	}
	details := &common.ComputeDetails{}
	if value, present := object.Fields["type"]; present {
		kind, ok := value.GetKind().(*structpb.Value_StringValue)
		if !ok {
			return 0, nil, fmt.Errorf("root Commitment type must be a string")
		}
		typed, err := concreteCommitmentType(kind.StringValue)
		if err != nil {
			return 0, nil, err
		}
		details.GCPCommitmentType = typed.String()
	}
	resources := object.Fields["resources"].GetListValue()
	if resources == nil {
		return 0, nil, fmt.Errorf("root Commitment resources must be an array")
	}
	count, memory, err := rootCommitmentResources(resources.Values)
	if err != nil {
		return 0, nil, err
	}
	details.MemoryGB = float64(memory) / 1024
	return count, details, nil
}

func populateRecommendationResources(gcpRec *recommenderpb.Recommendation, rec *common.Recommendation) error {
	count, details, err := rootCommitmentDetails(gcpRec.GetContent())
	if err != nil {
		return err
	}
	if details != nil {
		rec.Count, rec.Details = count, *details
		return nil
	}
	if err := requireSingleAmountResource(gcpRec.GetContent()); err != nil {
		return err
	}
	count, err = vcpuCountFromOperationGroups(gcpRec.GetContent())
	if err != nil {
		return err
	}
	rec.Count = count
	return extractMemoryMBFromRecommendation(gcpRec, rec)
}
