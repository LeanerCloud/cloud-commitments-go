package computeengine

import (
	"errors"
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

func rootResourceAmount(resource *structpb.Value) (kind string, amount int64, err error) {
	entry := resource.GetStructValue()
	if entry == nil {
		return "", 0, fmt.Errorf("root Commitment resource must be an object")
	}
	kind = entry.Fields["type"].GetStringValue()
	if kind != computepb.ResourceCommitment_VCPU.String() && kind != computepb.ResourceCommitment_MEMORY.String() {
		return "", 0, fmt.Errorf("root Commitment resource %q is unsupported by this VCPU+MEMORY client", kind)
	}
	if kind == computepb.ResourceCommitment_VCPU.String() {
		count, parseErr := recommendationVCPUAmount(entry.Fields["amount"])
		return kind, int64(count), parseErr
	}
	amount, err = recommendationAmount(entry.Fields["amount"], kind)
	return kind, amount, err
}

func rootCommitmentResources(resources []*structpb.Value) (count int, memory int64, err error) {
	seen := make(map[string]bool)
	for _, resource := range resources {
		kind, amount, parseErr := rootResourceAmount(resource)
		if parseErr != nil {
			return 0, 0, parseErr
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
		typed, typeErr := concreteCommitmentType(kind.StringValue)
		if typeErr != nil {
			return 0, nil, typeErr
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
	if resourceErr := requireSingleAmountResource(gcpRec.GetContent()); resourceErr != nil {
		return resourceErr
	}
	count, err = vcpuCountFromOperationGroups(gcpRec.GetContent())
	if err != nil {
		return err
	}
	rec.Count = count
	return extractMemoryMBFromRecommendation(gcpRec, rec)
}

// errBadPlan marks a recommendation whose commitment plan is malformed,
// missing where required, or in conflict with the caller's term. The
// recommendation is skipped rather than purchased with a guessed term.
var errBadPlan = errors.New("bad recommendation plan")

// rootCommitmentTerm reads the root Commitment's plan and returns the
// canonical term ("1yr" or "3yr"). found is false when the root has no plan
// key. A present but malformed plan returns an error wrapping errBadPlan.
func rootCommitmentTerm(content *recommenderpb.RecommendationContent) (term string, found bool, err error) {
	root, err := selectRootCommitment(content)
	if err != nil {
		return "", false, err
	}
	return rootPlanTerm(root)
}

func rootPlanTerm(root *recommenderpb.Operation) (term string, found bool, err error) {
	if root == nil {
		return "", false, nil
	}
	object := root.GetValue().GetStructValue()
	if object == nil {
		return "", false, fmt.Errorf("root Commitment value must be an object")
	}
	value, present := object.Fields["plan"]
	if !present {
		return "", false, nil
	}
	name, ok := value.GetKind().(*structpb.Value_StringValue)
	if !ok {
		return "", false, fmt.Errorf("%w: root Commitment plan must be a string", errBadPlan)
	}
	switch name.StringValue {
	case computepb.Commitment_TWELVE_MONTH.String():
		return "1yr", true, nil
	case computepb.Commitment_THIRTY_SIX_MONTH.String():
		return "3yr", true, nil
	default:
		return "", false, fmt.Errorf("%w: unsupported root Commitment plan %q", errBadPlan, name.StringValue)
	}
}

// canonicalTerm maps any accepted term spelling to "1yr" or "3yr". An
// unrecognized term is returned unchanged so the caller skips it, as before.
func canonicalTerm(term string) string {
	plan, err := termPlan(term)
	if err != nil {
		return term
	}
	if plan == computepb.Commitment_THIRTY_SIX_MONTH.String() {
		return "3yr"
	}
	return "1yr"
}

// resolveRecTerm picks the canonical term ("1yr"/"3yr") for a recommendation.
// A root Commitment plan is authoritative: an explicit params.Term must agree
// with it. A root Commitment without a plan needs an explicit params.Term. A
// payload with no root Commitment keeps the params.Term / "1yr" fallback
// (issue #291 follow-up).
func resolveRecTerm(gcpRec *recommenderpb.Recommendation, params common.RecommendationParams) (string, error) {
	root, err := selectRootCommitment(gcpRec.GetContent())
	if err != nil {
		return "", err
	}
	planTerm, found, err := rootPlanTerm(root)
	if err != nil {
		return "", err
	}
	if !found {
		return termWithoutPlan(root != nil, params.Term)
	}
	if params.Term == "" {
		return planTerm, nil
	}
	if want := canonicalTerm(params.Term); want != "1yr" && want != "3yr" {
		return want, nil
	} else if want != planTerm {
		return "", fmt.Errorf("%w: params term %q conflicts with recommendation plan term %s", errBadPlan, params.Term, planTerm)
	}
	return planTerm, nil
}

// termWithoutPlan resolves the term when the payload carries no plan.
func termWithoutPlan(hasRoot bool, paramTerm string) (string, error) {
	if paramTerm != "" {
		return canonicalTerm(paramTerm), nil
	}
	if hasRoot {
		return "", fmt.Errorf("%w: root Commitment has no plan and no explicit term was given", errBadPlan)
	}
	return "1yr", nil
}
