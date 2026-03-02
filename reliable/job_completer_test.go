package reliable

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestJobCompleterCompleteOneDone(t *testing.T) {
	claimed := false
	completed := false
	c := &JobCompleter[string]{
		ClaimDue: func(_ context.Context, _ time.Time) (*ClaimedJob[string], error) {
			claimed = true
			return &ClaimedJob[string]{ID: 7, Attempt: 1, Value: "ok"}, nil
		},
		Handle: func(_ context.Context, job *ClaimedJob[string]) error {
			if job == nil || job.Value != "ok" {
				t.Fatalf("unexpected job: %#v", job)
			}
			return nil
		},
		Complete: func(_ context.Context, job *ClaimedJob[string], _ time.Time) error {
			if job.ID != 7 {
				t.Fatalf("unexpected id: %d", job.ID)
			}
			completed = true
			return nil
		},
		Retry: func(_ context.Context, _ *ClaimedJob[string], _ time.Time, _ error, _ time.Duration) error {
			t.Fatalf("retry should not be called")
			return nil
		},
		Fail: func(_ context.Context, _ *ClaimedJob[string], _ time.Time, _ error) error {
			t.Fatalf("fail should not be called")
			return nil
		},
		ShouldRetry: func(error) bool { return false },
	}

	out, err := c.CompleteOne(context.Background())
	if err != nil {
		t.Fatalf("complete one: %v", err)
	}
	if !claimed || !completed {
		t.Fatalf("expected claim+complete callbacks to be called")
	}
	if !out.Claimed || out.JobID != 7 || out.Attempt != 1 {
		t.Fatalf("unexpected outcome: %#v", out)
	}
}

func TestJobCompleterCompleteOneRetry(t *testing.T) {
	expectedErr := errors.New("transient")
	retried := false
	c := &JobCompleter[int]{
		ClaimDue: func(_ context.Context, _ time.Time) (*ClaimedJob[int], error) {
			return &ClaimedJob[int]{ID: 9, Attempt: 3, Value: 1}, nil
		},
		Handle: func(_ context.Context, _ *ClaimedJob[int]) error {
			return expectedErr
		},
		Complete: func(_ context.Context, _ *ClaimedJob[int], _ time.Time) error {
			t.Fatalf("complete should not be called")
			return nil
		},
		Retry: func(_ context.Context, job *ClaimedJob[int], _ time.Time, cause error, delay time.Duration) error {
			if job.ID != 9 {
				t.Fatalf("unexpected job id: %d", job.ID)
			}
			if !errors.Is(cause, expectedErr) {
				t.Fatalf("unexpected cause: %v", cause)
			}
			if delay != 3*time.Second {
				t.Fatalf("unexpected delay: %s", delay)
			}
			retried = true
			return nil
		},
		Fail: func(_ context.Context, _ *ClaimedJob[int], _ time.Time, _ error) error {
			t.Fatalf("fail should not be called")
			return nil
		},
		ShouldRetry: func(error) bool { return true },
		Backoff: func(attempt int) time.Duration {
			return time.Duration(attempt) * time.Second
		},
	}

	out, err := c.CompleteOne(context.Background())
	if err != nil {
		t.Fatalf("complete one: %v", err)
	}
	if !retried {
		t.Fatalf("retry callback was not called")
	}
	if !out.Claimed || out.JobID != 9 || out.Attempt != 3 {
		t.Fatalf("unexpected outcome: %#v", out)
	}
}

func TestJobCompleterCompleteOneNoJob(t *testing.T) {
	c := &JobCompleter[struct{}]{
		ClaimDue: func(_ context.Context, _ time.Time) (*ClaimedJob[struct{}], error) { return nil, nil },
		Handle: func(_ context.Context, _ *ClaimedJob[struct{}]) error {
			t.Fatalf("handle should not be called")
			return nil
		},
		Complete: func(_ context.Context, _ *ClaimedJob[struct{}], _ time.Time) error {
			t.Fatalf("complete should not be called")
			return nil
		},
		Retry: func(_ context.Context, _ *ClaimedJob[struct{}], _ time.Time, _ error, _ time.Duration) error {
			return nil
		},
		Fail:        func(_ context.Context, _ *ClaimedJob[struct{}], _ time.Time, _ error) error { return nil },
		ShouldRetry: func(error) bool { return false },
	}

	out, err := c.CompleteOne(context.Background())
	if err != nil {
		t.Fatalf("complete one: %v", err)
	}
	if out.Claimed {
		t.Fatalf("expected no claimed job")
	}
}
