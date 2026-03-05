package gateway

import (
	"errors"
	"testing"
)

func TestIsRetryableInboundErrorDeadlineExceeded(t *testing.T) {
	err := errors.New(`anthropic request failed: Post "https://api.anthropic.com/v1/messages": context deadline exceeded`)
	if !isRetryableInboundError(err) {
		t.Fatalf("expected deadline exceeded error to be retryable")
	}
}
