package reservations

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/LeanerCloud/cloud-commitments-go/pkg/common"
)

// buildTaggedDisplayName runs a request body through ApplyPurchaseTags the
// way every service client does and returns the properties.displayName that
// would be sent to calculatePrice/purchase.
func buildTaggedDisplayName(t *testing.T, displayName, token string) string {
	t.Helper()
	body := map[string]interface{}{
		"properties": map[string]interface{}{"displayName": displayName},
	}
	ApplyPurchaseTags(body, common.PurchaseSourceWeb, token)
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	var decoded struct {
		Properties struct {
			DisplayName string `json:"displayName"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	return decoded.Properties.DisplayName
}

func vmDisplayName(now time.Time, randBytes []byte) string {
	return BuildDisplayName(DisplayNameFields{
		Service: "vm", Region: "eastus", ResourceType: "Standard_D2a_v4",
		Count: 1, Term: "1yr", Payment: "all-upfront", Now: now,
	}.WithRandSource(randBytes))
}

// TestDoIdempotentPurchaseTwoStep_UntaggedOrderMatchedByDisplayName is the
// issue #290 regression: the reservation order model (armreservations
// ReservationOrderResponse) defines no tags, so a list response can return the
// order bought by a first attempt without the idempotency tag. The re-drive
// must still find it through properties.displayName, which the purchase
// request and the order both define, and must not buy again. The re-drive
// rebuilds the display name with a new timestamp and random suffix, so the
// match cannot depend on those segments.
func TestDoIdempotentPurchaseTwoStep_UntaggedOrderMatchedByDisplayName(t *testing.T) {
	t.Parallel()
	const token = "4f1c2b0e9d8a7c6b5a4f3e2d1c0b9a8f7e6d5c4b3a2f1e0d9c8b7a6f5e4d3c2b"
	firstAttempt := buildTaggedDisplayName(t,
		vmDisplayName(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), []byte{0xaa, 0xbb, 0xcc, 0xdd}), token)

	// Azure stored the display name but returned no tags at all.
	listBody := `{"value":[{"name":"order-bought-earlier","properties":{"provisioningState":"Succeeded","displayName":"` + firstAttempt + `"}}]}`

	m := &mockHTTPClient{}
	listResp := fakeResp(http.StatusOK, listBody)
	t.Cleanup(func() { require.NoError(t, listResp.Body.Close()) })
	m.On("Do", mock.MatchedBy(func(r *http.Request) bool {
		return r.Method == http.MethodGet && r.URL.String() == listURL
	})).Return(listResp, nil).Once()

	// Purchase calls stay mocked so a regression fails on the assertions
	// below rather than on an unexpected mock call.
	calcResp := fakeResp(http.StatusOK, `{"properties":{"billingCurrencyTotal":{"currencyCode":"USD"},"reservationOrderId":"order-duplicate"}}`)
	t.Cleanup(func() { require.NoError(t, calcResp.Body.Close()) })
	m.On("Do", mock.MatchedBy(func(r *http.Request) bool {
		return r.Method == http.MethodPost && r.URL.String() == calcURL
	})).Return(calcResp, nil).Maybe()
	purchaseResp := fakeResp(http.StatusOK, `{}`)
	t.Cleanup(func() { require.NoError(t, purchaseResp.Body.Close()) })
	m.On("Do", mock.MatchedBy(func(r *http.Request) bool {
		return r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/purchase")
	})).Return(purchaseResp, nil).Maybe()

	redriveBody, err := json.Marshal(map[string]interface{}{
		"properties": map[string]interface{}{
			"displayName": buildTaggedDisplayName(t,
				vmDisplayName(time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC), []byte{0x11, 0x22, 0x33, 0x44}), token),
		},
	})
	require.NoError(t, err)

	orderID, existing, err := DoIdempotentPurchaseTwoStep(context.Background(), m, calcURL, redriveBody, "bearer", token, "")
	require.NoError(t, err)
	assert.True(t, existing, "the order bought by the first attempt must be adopted")
	assert.Equal(t, "order-bought-earlier", orderID)
	m.AssertNotCalled(t, "Do", mock.MatchedBy(func(r *http.Request) bool {
		return r.Method == http.MethodPost
	}))
}

// TestFindReservationOrderByIdempotencyToken_DisplayNameOfOtherTokenDoesNotMatch
// guards the opposite direction: an untagged order carrying a different
// token's marker must not be adopted.
func TestFindReservationOrderByIdempotencyToken_DisplayNameOfOtherTokenDoesNotMatch(t *testing.T) {
	t.Parallel()
	other := buildTaggedDisplayName(t, vmDisplayName(time.Now(), nil), "other-token")
	listBody := `{"value":[{"name":"order-other","properties":{"provisioningState":"Succeeded","displayName":"` + other + `"}}]}`

	m := &mockHTTPClient{}
	listResp := fakeResp(http.StatusOK, listBody)
	t.Cleanup(func() { require.NoError(t, listResp.Body.Close()) })
	m.On("Do", mock.Anything).Return(listResp, nil).Once()

	orderID, found, err := FindReservationOrderByIdempotencyToken(context.Background(), m, "bearer", "wanted-token")
	require.NoError(t, err)
	assert.False(t, found)
	assert.Empty(t, orderID)
}

// TestApplyPurchaseTags_DisplayNameMarker pins the shape of the display name
// written when a token is present: within Azure's 64-char allowlist, keeps
// the leading identifying segments, and ends with the same marker for the
// same token however the rest of the name was built.
func TestApplyPurchaseTags_DisplayNameMarker(t *testing.T) {
	t.Parallel()
	const token = "tok-290"
	marker := idempotencyDisplayNameMarker(token)

	a := buildTaggedDisplayName(t, vmDisplayName(time.Now(), []byte{1, 2, 3, 4}), token)
	b := buildTaggedDisplayName(t, vmDisplayName(time.Now().Add(time.Hour), []byte{5, 6, 7, 8}), token)
	for _, got := range []string{a, b} {
		assert.Regexp(t, `^[A-Za-z0-9_-]{1,64}$`, got)
		assert.True(t, strings.HasPrefix(got, "vm-eastus-Standard_D2a_v4-1x-1yr"), got)
		assert.True(t, strings.HasSuffix(got, "-"+marker), got)
		assert.True(t, displayNameCarriesToken(got, token))
	}
	assert.NotEqual(t, marker, idempotencyDisplayNameMarker("tok-other"))

	// A name that is already at the cap with no separators is truncated.
	long := buildTaggedDisplayName(t, strings.Repeat("x", 64), token)
	assert.Len(t, long, azureDisplayNameMaxLen)
	assert.True(t, displayNameCarriesToken(long, token))

	// A short name with spaces (synapse/managedredis style) is kept intact.
	short := buildTaggedDisplayName(t, "Synapse SQL Pool Reservation - DW100c", token)
	assert.Equal(t, "Synapse SQL Pool Reservation - DW100c-"+marker, short)

	// Missing displayName: the marker alone is written.
	assert.Equal(t, marker, buildTaggedDisplayName(t, "", token))
}

// TestApplyPurchaseTags_NoTokenLeavesDisplayNameAlone keeps the untokened
// paths (CLI legacy, source-only) byte-identical to before issue #290.
func TestApplyPurchaseTags_NoTokenLeavesDisplayNameAlone(t *testing.T) {
	t.Parallel()
	name := vmDisplayName(time.Now(), []byte{1, 2, 3, 4})
	assert.Equal(t, name, buildTaggedDisplayName(t, name, ""))
}

// TestWithIdempotencyMarker_TruncatesAtRuneBoundary covers a long display
// name with no '-' whose hard truncation point falls inside a multibyte
// rune: the cut must back off to the rune start so the name stays valid
// UTF-8 (JSON encoding would otherwise widen it past the byte limit).
func TestWithIdempotencyMarker_TruncatesAtRuneBoundary(t *testing.T) {
	t.Parallel()
	const token = "tok-290"
	marker := idempotencyDisplayNameMarker(token)
	room := azureDisplayNameMaxLen - len(marker) - 1

	// 'é' is two bytes; placing it at room-1 puts the cut between its bytes.
	name := strings.Repeat("x", room-1) + "é" + strings.Repeat("y", 10)
	got := withIdempotencyMarker(name, token)

	assert.True(t, utf8.ValidString(got), "result must be valid UTF-8: %q", got)
	assert.LessOrEqual(t, len(got), azureDisplayNameMaxLen)
	assert.Equal(t, strings.Repeat("x", room-1)+"-"+marker, got)
	assert.True(t, displayNameCarriesToken(got, token))
}

func TestTruncateToRuneBoundary(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", truncateToRuneBoundary("abc", 5))
	assert.Equal(t, "ab", truncateToRuneBoundary("abc", 2))
	assert.Equal(t, "a", truncateToRuneBoundary("aé", 2))
	assert.Equal(t, "aé", truncateToRuneBoundary("aéb", 3))
	assert.Equal(t, "", truncateToRuneBoundary("€", 2))
}
