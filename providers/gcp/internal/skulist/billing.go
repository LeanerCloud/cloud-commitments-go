package skulist

import (
	"context"
	"fmt"

	"google.golang.org/api/cloudbilling/v1"
	"google.golang.org/api/option"
)

// BillingService lists the SKUs of one Cloud Billing service (enables mocking).
type BillingService interface {
	ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error)
}

// realBillingService lists SKUs through the Cloud Billing API; name labels the
// owning GCP service in errors.
type realBillingService struct {
	service *cloudbilling.APIService
	name    string
}

func (r *realBillingService) ListSKUs(serviceID string) (*cloudbilling.ListSkusResponse, error) {
	skus, err := All(context.Background(), r.service.Services.Skus.List(serviceID), DefaultMaxPages)
	if err != nil {
		return nil, fmt.Errorf("%s: list SKUs for %s: %w", r.name, serviceID, err)
	}
	return &cloudbilling.ListSkusResponse{Skus: skus}, nil
}

// Resolve returns existing when set, else a BillingService backed by a new
// Cloud Billing client. name labels the owning GCP service in errors.
func Resolve(ctx context.Context, existing BillingService, name string, opts ...option.ClientOption) (BillingService, error) {
	if existing != nil {
		return existing, nil
	}
	service, err := cloudbilling.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create billing service: %w", err)
	}
	return &realBillingService{service: service, name: name}, nil
}
