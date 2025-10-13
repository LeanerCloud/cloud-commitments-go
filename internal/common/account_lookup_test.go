package common

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/stretchr/testify/assert"
)

func TestNewAccountAliasCache(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	cache := NewAccountAliasCache(cfg)

	assert.NotNil(t, cache)
	assert.NotNil(t, cache.cache)
	assert.NotNil(t, cache.client)
}

func TestGetAccountAlias(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	cache := NewAccountAliasCache(cfg)

	// Test the cache behavior by calling it twice and verifying consistency
	ctx := context.Background()

	alias1 := cache.GetAccountAlias(ctx, "123456789012")
	alias2 := cache.GetAccountAlias(ctx, "123456789012")

	// Both calls should return the same result
	assert.Equal(t, alias1, alias2)

	// Test with empty account ID
	alias3 := cache.GetAccountAlias(ctx, "")
	assert.Equal(t, "", alias3)
}

func TestGetAccountAliasShort(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	cache := NewAccountAliasCache(cfg)
	ctx := context.Background()

	tests := []struct {
		name      string
		accountID string
	}{
		{
			name:      "Test short alias",
			accountID: "123456789012",
		},
		{
			name:      "Empty account ID",
			accountID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := cache.GetAccountAliasShort(ctx, tt.accountID)
			// Just verify it doesn't panic and returns a string
			assert.LessOrEqual(t, len(result), 15)
		})
	}
}

func TestSanitizeForReservationID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Simple lowercase",
			input:    "production",
			expected: "production",
		},
		{
			name:     "Uppercase preserved",
			input:    "PRODUCTION",
			expected: "PRODUCTION",
		},
		{
			name:     "Spaces to hyphens",
			input:    "my account",
			expected: "my-account",
		},
		{
			name:     "Underscores to hyphens",
			input:    "my_account",
			expected: "my-account",
		},
		{
			name:     "Remove invalid characters",
			input:    "my@account#123",
			expected: "myaccount123",
		},
		{
			name:     "Dots to hyphens",
			input:    "my.account",
			expected: "my-account",
		},
		{
			name:     "Mixed valid characters",
			input:    "MyAccount123",
			expected: "MyAccount123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeForReservationID(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestPreloadAccountAliases(t *testing.T) {
	cfg := aws.Config{Region: "us-east-1"}
	cache := NewAccountAliasCache(cfg)

	ctx := context.Background()
	accountIDs := []string{"123456789012", "210987654321"}

	// This will likely fail in test environment without AWS credentials,
	// but we're testing that it doesn't panic
	cache.PreloadAccountAliases(ctx, accountIDs)

	// Verify cache was populated (the actual values may be empty or account IDs
	// if the AWS API call fails)
	for _, id := range accountIDs {
		alias := cache.GetAccountAlias(ctx, id)
		// Just verify it returns something and doesn't panic
		_ = alias
	}
}
