package common //nolint:revive // pkg/common is the module's core domain package (Recommendation, ProviderType, ...), imported by name (common.X) throughout this repo and by consumer-mcp; renaming would break every consumer import

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var testFixedNow = time.Date(2026, 5, 21, 0, 20, 19, 0, time.UTC)

func testFixedRand() []byte { return []byte{0xa1, 0xb2, 0xc3, 0xd4} }

func TestBuildReservationName_HappyPath(t *testing.T) {
	t.Parallel()
	got := BuildReservationName(ReservationNameFields{
		Service:      "opensearch",
		Region:       "us-east-1",
		ResourceType: "r6g.large.search",
		Count:        3,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "opensearch-reserved-")

	// The composed full name is too long for the 60-char cap; the builder
	// drops the timestamp to fit while keeping the random suffix. The
	// high-signal required segments (svc/region/sku/count/term) and the
	// payment-option always remain.
	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen)
	assert.True(t, strings.HasPrefix(got, "opensearch-us-east-1-r6g-large-search-3x-1yr"),
		"required segments must be present: got %q", got)
	assert.Contains(t, got, "allup", "payment normalization must survive")
	assert.True(t, strings.HasSuffix(got, "-a1b2c3d4"), "uniqueness suffix must survive: got %q", got)
}

func TestBuildReservationName_ShortInput_FullName(t *testing.T) {
	t.Parallel()
	// Short fields fit comfortably under the cap; verify the exact format.
	got := BuildReservationName(ReservationNameFields{
		Service:      "rds",
		Region:       "us-east-1",
		ResourceType: "db.medium",
		Count:        1,
		Term:         "1yr",
		Payment:      "no-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "rds-reserved-")

	// Full assembled name: "rds-us-east-1-db-medium-1x-1yr-noup-20260521T002019-a1b2c3d4"
	// = 60 chars exactly.
	assert.Equal(t, "rds-us-east-1-db-medium-1x-1yr-noup-20260521T002019-a1b2c3d4", got)
	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen)
}

func TestBuildReservationName_LengthFit_DropsTimestampFirst(t *testing.T) {
	t.Parallel()
	// Pick fields that compose just barely over 60 with the rand suffix but
	// fit without it: svc(rds=3) + region(eu-west-1=9) + sku("m6gd.xlarge"->"m6gd-xlarge"=11) +
	// count(1x=2) + term(1yr=3) + paymt(allup=5) + ts(15) + rand(8) + 7 separators
	// = 3+9+11+2+3+5+15+8 + 7 = 63 chars. Without rand: 63-8-1 = 54. Fits.
	got := BuildReservationName(ReservationNameFields{
		Service:      "rds",
		Region:       "eu-west-1",
		ResourceType: "m6gd.xlarge",
		Count:        1,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "rds-reserved-")

	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen)
	assert.Contains(t, got, "a1b2c3d4", "random suffix must never drop")
	assert.NotContains(t, got, "20260521T002019", "timestamp should be the first to drop")
	assert.Contains(t, got, "allup", "payment should still be present")
}

func TestBuildReservationName_LengthFit_DropsTimestampNext(t *testing.T) {
	t.Parallel()
	// Pick a length where rand-dropped still overflows by ts-and-not-payment.
	// With cache service code (5) + region (us-east-1 = 9) + a 24-char SKU,
	// count + term + payment + ts = ~24+9+5+2+3+5+15 + 6 seps = 69; drop
	// rand (8+1=-9) -> 60, exactly at the cap, keeps timestamp.
	// To push ts off but keep payment, use a 25-char SKU.
	got := BuildReservationName(ReservationNameFields{
		Service:      "cache",
		Region:       "us-east-1",
		ResourceType: "cache.r6gd.xlarge",
		Count:        2,
		Term:         "3yr",
		Payment:      "partial-upfront", // -> "partup" (6 chars)
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "cache-reserved-")

	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen)
	assert.Contains(t, got, "a1b2c3d4", "rand must never drop")
	assert.NotContains(t, got, "20260521T002019", "ts should drop")
	assert.Contains(t, got, "partup", "payment should still survive")
	assert.Contains(t, got, "2x-3yr", "count+term must never drop")
}

func TestBuildReservationName_LengthFit_DropsPaymentLast(t *testing.T) {
	t.Parallel()
	// Compose so that even dropping rand+ts is not enough, forcing
	// payment to drop too. Required-only must still fit at ≤60.
	got := BuildReservationName(ReservationNameFields{
		Service:      "opensearch",
		Region:       "ap-northeast-1",
		ResourceType: "r6gd.16xlarge.search.opensearch",
		Count:        12,
		Term:         "3yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "opensearch-reserved-")

	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen)
	assert.True(t, strings.HasPrefix(got, "opensearch-ap-northeast-1-"),
		"service and region must never drop")
	assert.Contains(t, got, "12x", "count must never drop")
	assert.Contains(t, got, "3yr", "term must never drop")
	assert.NotContains(t, got, "allup", "payment should be the last optional segment to drop")
	assert.NotContains(t, got, "20260521T002019", "ts should drop in this length regime")
	assert.Contains(t, got, "a1b2c3d4", "rand must never drop")
}

func TestBuildReservationName_LengthFit_WorstCase_TruncatesSKUNotCountTerm(t *testing.T) {
	t.Parallel()
	// Pathological: a SKU so long that even all optional segments dropped
	// leaves us over 60. The builder must truncate the SKU itself rather
	// than the joined string, so count and term still survive the cap.
	got := BuildReservationName(ReservationNameFields{
		Service:      "opensearch",
		Region:       "ap-northeast-1",
		ResourceType: strings.Repeat("super-long-sku-", 6),
		Count:        99,
		Term:         "3yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "opensearch-reserved-")

	assert.LessOrEqual(t, len(got), awsReservationNameMaxLen, "must never exceed the cap")
	assert.False(t, strings.HasSuffix(got, "-"), "must not end with a hyphen after truncation: %q", got)
	assert.True(t, strings.HasPrefix(got, "opensearch-ap-northeast-1-"),
		"service and region must never drop, even in worst case")
	assert.Contains(t, got, "99x", "count must survive SKU truncation")
	assert.Contains(t, got, "3yr", "term must survive SKU truncation")
	assert.True(t, strings.HasSuffix(got, "-a1b2c3d4"), "suffix must survive SKU truncation: got %q", got)
}

// Issue #123: a long SKU used to drop the uniqueness suffix first, so two
// purchases of the same SKU produced the same (idempotency) name.
func TestBuildReservationName_LongSKU_KeepsUniquenessSuffix(t *testing.T) {
	t.Parallel()
	f := ReservationNameFields{
		Service:      "opensearch",
		Region:       "us-east-1",
		ResourceType: "r6gd.large.search",
		Count:        3,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}
	a := BuildReservationName(f.WithRandSource([]byte{1, 2, 3, 4}), "opensearch-reserved-")
	b := BuildReservationName(f.WithRandSource([]byte{5, 6, 7, 8}), "opensearch-reserved-")
	assert.NotEqual(t, a, b, "same SKU, different suffix must not collide")
	assert.LessOrEqual(t, len(a), awsReservationNameMaxLen)
	assert.True(t, strings.HasSuffix(a, "-01020304"), "got %q", a)
	assert.True(t, strings.HasSuffix(b, "-05060708"), "got %q", b)
}

func TestBuildReservationName_PaymentNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"all-upfront", "allup"},
		{"upfront", "allup"},
		{"no-upfront", "noup"},
		{"partial-upfront", "partup"},
		{"partial", "partup"},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := normalizeReservationPayment(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildReservationName_PaymentUnknown_TruncatedAndSanitized(t *testing.T) {
	t.Parallel()
	got := normalizeReservationPayment("monthly_billing")
	// Dropped underscores, then truncated to 6 chars.
	assert.LessOrEqual(t, len(got), 6)
	assert.Regexp(t, regexp.MustCompile(`^[a-zA-Z0-9-]*$`), got)
}

func TestBuildReservationName_SKUDotsToHyphens(t *testing.T) {
	t.Parallel()
	got := BuildReservationName(ReservationNameFields{
		Service:      "rds",
		Region:       "us-east-1",
		ResourceType: "db.t4g.medium",
		Count:        1,
		Term:         "1yr",
		Payment:      "no-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "rds-reserved-")

	assert.Contains(t, got, "db-t4g-medium", "dots in SKU must become hyphens")
	assert.NotContains(t, got, "db.t4g.medium")
}

func TestBuildReservationName_TermNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"1yr", "1yr"},
		{"1y", "1yr"},
		{"1", "1yr"},
		{"P1Y", "1yr"},
		{"3yr", "3yr"},
		{"3y", "3yr"},
		{"3", "3yr"},
		{"P3Y", "3yr"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			got := normalizeReservationTerm(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildReservationName_DeterministicWithFixedSources(t *testing.T) {
	t.Parallel()
	fields := ReservationNameFields{
		Service:      "rds",
		Region:       "us-east-1",
		ResourceType: "db.t4g.medium",
		Count:        1,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand())

	a := BuildReservationName(fields, "rds-reserved-")
	b := BuildReservationName(fields, "rds-reserved-")
	assert.Equal(t, a, b, "same inputs (incl. fixed Now + rand) must yield the same output")
}

func TestBuildReservationName_AlwaysSanitized(t *testing.T) {
	t.Parallel()
	got := BuildReservationName(ReservationNameFields{
		Service:      "rds",
		Region:       "us-east-1",
		ResourceType: "db.t4g.medium",
		Count:        1,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          testFixedNow,
	}.WithRandSource(testFixedRand()), "rds-reserved-")

	// AWS reservation-name allowlist is [a-zA-Z0-9-].
	assert.Regexp(t, regexp.MustCompile(`^[a-zA-Z0-9-]+$`), got)
	assert.NotContains(t, got, "--", "must not contain consecutive hyphens")
	assert.False(t, strings.HasPrefix(got, "-"), "must not start with a hyphen")
	assert.False(t, strings.HasSuffix(got, "-"), "must not end with a hyphen")
}

func TestBuildReservationName_NowInUTC(t *testing.T) {
	t.Parallel()
	// Pass a non-UTC time; builder must normalize to UTC for the timestamp.
	loc, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("LoadLocation unavailable")
	}
	localTime := time.Date(2026, 5, 20, 17, 20, 19, 0, loc) // = 2026-05-21T00:20:19Z
	got := BuildReservationName(ReservationNameFields{
		Service:      "rds",
		Region:       "us-east-1",
		ResourceType: "db.t4g.m",
		Count:        1,
		Term:         "1yr",
		Payment:      "all-upfront",
		Now:          localTime,
	}.WithRandSource(testFixedRand()), "rds-reserved-")

	assert.Contains(t, got, "20260521T002019", "timestamp must be UTC-formatted: %q", got)
}

func TestBuildReservationName_PerServicePrefixes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		svc    string
		prefix string
	}{
		{"rds", "rds-"},
		{"cache", "cache-"},
		{"memdb", "memdb-"},
		{"opensearch", "opensearch-"},
		{"redshift", "redshift-"},
	}
	for _, tc := range cases {
		t.Run(tc.svc, func(t *testing.T) {
			t.Parallel()
			got := BuildReservationName(ReservationNameFields{
				Service:      tc.svc,
				Region:       "us-east-1",
				ResourceType: "x.large",
				Count:        1,
				Term:         "1yr",
				Payment:      "all-upfront",
				Now:          testFixedNow,
			}.WithRandSource(testFixedRand()), fmt.Sprintf("%sreserved-", tc.prefix))

			assert.True(t, strings.HasPrefix(got, tc.prefix),
				"service code must lead the name: got %q want prefix %q", got, tc.prefix)
		})
	}
}
