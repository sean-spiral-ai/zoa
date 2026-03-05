package reliable

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

type ClaimedJob[T any] struct {
	ID      int64
	Attempt int
	Value   T
}

type JobOutcome struct {
	Claimed bool
	JobID   int64
	Attempt int
}

type JobCompleter[T any] struct {
	ClaimDue    func(ctx context.Context, now time.Time) (*ClaimedJob[T], error)
	Handle      func(ctx context.Context, job *ClaimedJob[T]) error
	Complete    func(ctx context.Context, job *ClaimedJob[T], now time.Time) error
	Retry       func(ctx context.Context, job *ClaimedJob[T], now time.Time, cause error, delay time.Duration) error
	Fail        func(ctx context.Context, job *ClaimedJob[T], now time.Time, cause error) error
	ShouldRetry func(err error) bool
	Backoff     func(attempt int) time.Duration
	MaxAttempts int // 0 means no limit
	Logger      *slog.Logger
}

func (c *JobCompleter[T]) logger() *slog.Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return slog.Default()
}

func (c *JobCompleter[T]) CompleteOne(ctx context.Context) (JobOutcome, error) {
	if c == nil {
		return JobOutcome{}, fmt.Errorf("job completer is nil")
	}
	if c.ClaimDue == nil {
		return JobOutcome{}, fmt.Errorf("claim due callback is nil")
	}
	if c.Handle == nil {
		return JobOutcome{}, fmt.Errorf("handle callback is nil")
	}
	if c.Complete == nil {
		return JobOutcome{}, fmt.Errorf("complete callback is nil")
	}
	if c.Retry == nil {
		return JobOutcome{}, fmt.Errorf("retry callback is nil")
	}
	if c.Fail == nil {
		return JobOutcome{}, fmt.Errorf("fail callback is nil")
	}
	if c.ShouldRetry == nil {
		return JobOutcome{}, fmt.Errorf("should retry callback is nil")
	}

	if ctx == nil {
		ctx = context.Background()
	}
	log := c.logger()
	claimNow := time.Now().UTC()
	job, err := c.ClaimDue(ctx, claimNow)
	if err != nil {
		return JobOutcome{}, err
	}
	if job == nil {
		return JobOutcome{}, nil
	}
	outcome := JobOutcome{
		Claimed: true,
		JobID:   job.ID,
		Attempt: job.Attempt,
	}

	log.Debug("job claimed", "job_id", job.ID, "attempt", job.Attempt)

	if err := c.Handle(ctx, job); err != nil {
		callbackNow := time.Now().UTC()
		if c.ShouldRetry(err) && (c.MaxAttempts <= 0 || job.Attempt < c.MaxAttempts) {
			delay := time.Duration(0)
			if c.Backoff != nil {
				delay = c.Backoff(job.Attempt)
			}
			if delay < 0 {
				delay = 0
			}
			log.Warn("job retry",
				"job_id", job.ID,
				"attempt", job.Attempt,
				"delay", delay,
				"error", err,
			)
			if retryErr := c.Retry(ctx, job, callbackNow, err, delay); retryErr != nil {
				return outcome, retryErr
			}
			return outcome, nil
		}
		log.Error("job failed",
			"job_id", job.ID,
			"attempt", job.Attempt,
			"error", err,
		)
		if failErr := c.Fail(ctx, job, callbackNow, err); failErr != nil {
			return outcome, failErr
		}
		return outcome, nil
	}

	if job.Attempt > 1 {
		log.Info("job completed after retries",
			"job_id", job.ID,
			"attempt", job.Attempt,
		)
	}

	completeNow := time.Now().UTC()
	if err := c.Complete(ctx, job, completeNow); err != nil {
		return outcome, err
	}
	return outcome, nil
}
