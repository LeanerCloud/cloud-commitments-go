package common

import (
	"context"
	"math"
	"time"
)

// RateLimiter provides rate limiting with exponential backoff
type RateLimiter struct {
	// Base delay between requests
	baseDelay time.Duration
	// Maximum delay for exponential backoff
	maxDelay time.Duration
	// Current retry attempt
	retryCount int
	// Maximum number of retries
	maxRetries int
}

// NewRateLimiter creates a new rate limiter with default settings
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		baseDelay:  1 * time.Second,
		maxDelay:   30 * time.Second,
		maxRetries: 5,
		retryCount: 0,
	}
}

// NewRateLimiterWithOptions creates a rate limiter with custom settings
func NewRateLimiterWithOptions(baseDelay, maxDelay time.Duration, maxRetries int) *RateLimiter {
	return &RateLimiter{
		baseDelay:  baseDelay,
		maxDelay:   maxDelay,
		maxRetries: maxRetries,
		retryCount: 0,
	}
}

// Wait implements exponential backoff delay
func (r *RateLimiter) Wait(ctx context.Context) error {
	if r.retryCount == 0 {
		// No delay for first attempt
		return nil
	}

	// Calculate exponential backoff with jitter
	backoffSeconds := math.Pow(2, float64(r.retryCount-1))
	delay := time.Duration(backoffSeconds) * r.baseDelay

	// Cap at maximum delay
	if delay > r.maxDelay {
		delay = r.maxDelay
	}

	// Add jitter (up to 20% of delay)
	jitter := time.Duration(float64(delay) * 0.2 * math.Sin(float64(time.Now().UnixNano())))
	if jitter < 0 {
		jitter = -jitter
	}
	delay += jitter

	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ShouldRetry checks if we should retry based on error and retry count
func (r *RateLimiter) ShouldRetry(err error) bool {
	if err == nil {
		r.Reset()
		return false
	}

	// Check if we've exceeded max retries
	if r.retryCount >= r.maxRetries {
		return false
	}

	// Check for retryable errors (you can expand this based on AWS error types)
	// For now, we'll retry on any error
	r.retryCount++
	return true
}

// Reset resets the retry counter
func (r *RateLimiter) Reset() {
	r.retryCount = 0
}

// GetRetryCount returns the current retry count
func (r *RateLimiter) GetRetryCount() int {
	return r.retryCount
}