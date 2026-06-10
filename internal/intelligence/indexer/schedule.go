package indexer

import (
	"context"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// RerunItem identifies a run that is due for rerun (e.g. after unstable evaluation).
type RerunItem struct {
	RunID  string
	RepoID string
}

// SchedulerOptions configure when and how the indexer run is triggered.
type SchedulerOptions struct {
	// Schedule is a cron expression (e.g. "0 1 * * *" for daily at 01:00). Empty = no recurring schedule.
	Schedule string
	// RunOnFirstStart runs the indexer once at startup when HasPreviousRun returns false for RepoID.
	RunOnFirstStart bool
	// RepoID is passed to HasPreviousRun to detect first run.
	RepoID string
	// Run is called for each scheduled or first-run execution.
	Run func(ctx context.Context) error
	// HasPreviousRun returns true if at least one index run exists for the repo (used when RunOnFirstStart is true).
	HasPreviousRun func(ctx context.Context, repoID string) (bool, error)
	// RerunCheckInterval: when > 0, a ticker runs that calls ListRunsDueForRerun and invokes Rerun for each. 0 = disabled.
	RerunCheckInterval time.Duration
	// ListRunsDueForRerun returns runs that are due for rerun (e.g. scheduled_rerun_at <= now). Used when RerunCheckInterval > 0.
	ListRunsDueForRerun func(ctx context.Context) ([]RerunItem, error)
	// Rerun is called for each run returned by ListRunsDueForRerun. Required when RerunCheckInterval > 0.
	Rerun func(ctx context.Context, runID, repoID string) error
}

// Scheduler runs the indexer on a cron schedule and optionally once on first start when no previous run exists.
type Scheduler struct {
	opts   SchedulerOptions
	cron   *cron.Cron
	mu     sync.Mutex
	stopCh chan struct{}
}

// NewScheduler creates a scheduler from the given options. Call Start to begin.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	if opts.HasPreviousRun == nil {
		opts.HasPreviousRun = func(context.Context, string) (bool, error) { return true, nil }
	}
	return &Scheduler{opts: opts, stopCh: make(chan struct{})}
}

// Start starts the scheduler: if RunOnFirstStart and no previous run for RepoID, runs once immediately;
// then if RerunCheckInterval > 0, starts a ticker for scheduled reruns; then if Schedule is set, starts the cron job.
// It blocks until Stop is called or ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	if s.opts.RunOnFirstStart && s.opts.RepoID != "" && s.opts.Run != nil {
		has, err := s.opts.HasPreviousRun(ctx, s.opts.RepoID)
		if err != nil {
			return err
		}
		if !has {
			if err := s.opts.Run(ctx); err != nil {
				return err
			}
		}
	}
	if s.opts.RerunCheckInterval > 0 && s.opts.ListRunsDueForRerun != nil && s.opts.Rerun != nil {
		go s.runRerunTicker(ctx)
	}
	if s.opts.Schedule == "" {
		<-ctx.Done()
		return ctx.Err()
	}
	// Standard 5-field cron: minute hour day-of-month month day-of-week (e.g. "0 1 * * *" = daily at 01:00)
	s.cron = cron.New()
	_, err := s.cron.AddFunc(s.opts.Schedule, func() {
		runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer cancel()
		_ = s.opts.Run(runCtx)
	})
	if err != nil {
		return err
	}
	s.cron.Start()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.stopCh:
		return nil
	}
}

func (s *Scheduler) runRerunTicker(ctx context.Context) {
	ticker := time.NewTicker(s.opts.RerunCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			due, err := s.opts.ListRunsDueForRerun(ctx)
			if err != nil {
				continue
			}
			for _, item := range due {
				runCtx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
				_ = s.opts.Rerun(runCtx, item.RunID, item.RepoID)
				cancel()
			}
		}
	}
}

// Stop stops the scheduler and any recurring cron.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
		s.cron = nil
	}
	select {
	case <-s.stopCh:
	default:
		close(s.stopCh)
	}
}
