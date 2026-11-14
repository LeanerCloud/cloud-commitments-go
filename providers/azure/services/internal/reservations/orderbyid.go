package reservations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/reservations/armreservations"
)

// ReservationOrderURL returns the get-reservation-order endpoint URL.
func ReservationOrderURL(reservationOrderID string) string {
	return fmt.Sprintf("%s/providers/Microsoft.Capacity/reservationOrders/%s?api-version=%s",
		BaseURL, url.PathEscape(reservationOrderID), apiVersion)
}

// getReservationOrderState GETs one reservation order by ID and returns its
// provisioning state. Unlike the list, a GET by ID is not subject to the list's
// propagation lag, which is why a completed purchase claim reads its recorded
// order this way (go#745).
func getReservationOrderState(ctx context.Context, httpClient HTTPClient, bearerToken, orderID string) (armreservations.ProvisioningState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ReservationOrderURL(orderID), http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build get reservation order request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+bearerToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("get reservation order HTTP call: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close() // #nosec G104 -- body fully drained by io.ReadAll before Close; transport close error does not affect correctness
	if err != nil {
		return "", fmt.Errorf("read get reservation order response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("get reservation order %q failed with status %d: %s", orderID, resp.StatusCode, string(body))
	}
	var order struct {
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(body, &order); err != nil {
		return "", fmt.Errorf("decode get reservation order response: %w", err)
	}
	return armreservations.ProvisioningState(order.Properties.ProvisioningState), nil
}

// adoptCompletedOrder decides what a completed purchase claim's recorded order
// means, through reservationOrderDispositions. An adoptable state returns the
// order ID. A rolled-back state (Failed, Canceled, Expired) is an error that
// names the order and state: the completed claim wins, so neither adopting nor
// buying again is right and a new execution is needed. A blocked state returns
// ErrReservationOrderBlocked, as the list lookup does. A failed read returns the
// read error: an unknown state is never adopted.
func adoptCompletedOrder(ctx context.Context, httpClient HTTPClient, bearerToken, orderID string) (string, error) {
	if orderID == "" {
		return "", fmt.Errorf("completed purchase claim records no reservation order ID")
	}
	state, err := getReservationOrderState(ctx, httpClient, bearerToken, orderID)
	if err != nil {
		return "", fmt.Errorf("read the order a completed purchase claim recorded: %w", err)
	}
	switch dispositionOf(state) {
	case orderRetryable:
		return "", fmt.Errorf("reservation order %q recorded by a completed purchase claim is in state %s; not adopting it and not buying again (start a new execution to purchase)", orderID, state)
	case orderBlocked:
		return "", fmt.Errorf("reservation order %q recorded by a completed purchase claim is in state %s; refusing to adopt it or buy again (resolve or cancel the order in Azure, or start a new execution): %w", orderID, state, ErrReservationOrderBlocked)
	case orderAdopt:
	}
	return orderID, nil
}
