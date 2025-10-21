package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	pkgSync "s3migration/pkg/sync"
)

// Re-export types from sync package for convenience
type (
	SyncOptions      = pkgSync.SyncOptions
	ConflictStrategy = pkgSync.ConflictStrategy
)

// Re-export constants
const (
	ConflictNewest = pkgSync.ConflictNewest
	ConflictSource = pkgSync.ConflictSource
	ConflictDest   = pkgSync.ConflictDest
	ConflictSkip   = pkgSync.ConflictSkip
	ConflictRename = pkgSync.ConflictRename
)

// Schedule represents a scheduled migration task
type Schedule struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	CronExpr    string        `json:"cron_expr"`
	Enabled     bool          `json:"enabled"`
	Source      SourceConfig  `json:"source"`
	Destination DestConfig    `json:"destination"`
	Options     SyncOptions   `json:"options"`
	LastRun     time.Time     `json:"last_run"`
	NextRun     time.Time     `json:"next_run"`
	RunCount    int           `json:"run_count"`
	FailCount   int           `json:"fail_count"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
}

// SourceConfig holds source bucket configuration
type SourceConfig struct {
	Provider    string            `json:"provider"`
	Bucket      string            `json:"bucket"`
	Prefix      string            `json:"prefix"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// DestConfig holds destination bucket configuration
type DestConfig struct {
	Provider    string            `json:"provider"`
	Bucket      string            `json:"bucket"`
	Prefix      string            `json:"prefix"`
	Credentials map[string]string `json:"credentials,omitempty"`
}

// Scheduler manages scheduled migration tasks
type Scheduler struct {
	mu        sync.RWMutex
	cron      *cron.Cron
	schedules map[string]*Schedule
	entries   map[string]cron.EntryID
	executor  TaskExecutor
	running   bool
}

// TaskExecutor interface for executing migrations
type TaskExecutor interface {
	Execute(ctx context.Context, schedule *Schedule) error
}

// NewScheduler creates a new scheduler
func NewScheduler(executor TaskExecutor) *Scheduler {
	return &Scheduler{
		cron:      cron.New(cron.WithSeconds()),
		schedules: make(map[string]*Schedule),
		entries:   make(map[string]cron.EntryID),
		executor:  executor,
	}
}

// Start starts the scheduler
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler already running")
	}

	s.cron.Start()
	s.running = true
	return nil
}

// Stop stops the scheduler
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return fmt.Errorf("scheduler not running")
	}

	ctx := s.cron.Stop()
	<-ctx.Done()
	s.running = false
	return nil
}

// AddSchedule adds a new scheduled task
func (s *Scheduler) AddSchedule(schedule *Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.schedules[schedule.ID]; exists {
		return fmt.Errorf("schedule %s already exists", schedule.ID)
	}

	// Parse cron expression
	cronSchedule, err := cron.ParseStandard(schedule.CronExpr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}

	// Set metadata
	now := time.Now()
	schedule.CreatedAt = now
	schedule.UpdatedAt = now
	schedule.NextRun = cronSchedule.Next(now)

	// Add to cron if enabled
	if schedule.Enabled {
		entryID, err := s.cron.AddFunc(schedule.CronExpr, func() {
			s.executeSchedule(schedule.ID)
		})
		if err != nil {
			return fmt.Errorf("failed to add cron job: %w", err)
		}
		s.entries[schedule.ID] = entryID
	}

	s.schedules[schedule.ID] = schedule
	return nil
}

// RemoveSchedule removes a scheduled task
func (s *Scheduler) RemoveSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, exists := s.schedules[id]
	if !exists {
		return fmt.Errorf("schedule %s not found", id)
	}

	// Remove from cron if active
	if entryID, exists := s.entries[id]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, id)
	}

	delete(s.schedules, id)
	return nil
}

// UpdateSchedule updates an existing schedule
func (s *Scheduler) UpdateSchedule(schedule *Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldSchedule, exists := s.schedules[schedule.ID]
	if !exists {
		return fmt.Errorf("schedule %s not found", schedule.ID)
	}

	// Preserve metadata
	schedule.CreatedAt = oldSchedule.CreatedAt
	schedule.RunCount = oldSchedule.RunCount
	schedule.FailCount = oldSchedule.FailCount
	schedule.UpdatedAt = time.Now()

	// Remove old cron entry
	if entryID, exists := s.entries[schedule.ID]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, schedule.ID)
	}

	// Add new cron entry if enabled
	if schedule.Enabled {
		entryID, err := s.cron.AddFunc(schedule.CronExpr, func() {
			s.executeSchedule(schedule.ID)
		})
		if err != nil {
			return fmt.Errorf("failed to update cron job: %w", err)
		}
		s.entries[schedule.ID] = entryID
	}

	s.schedules[schedule.ID] = schedule
	return nil
}

// GetSchedule retrieves a schedule by ID
func (s *Scheduler) GetSchedule(id string) (*Schedule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	schedule, exists := s.schedules[id]
	if !exists {
		return nil, fmt.Errorf("schedule %s not found", id)
	}

	return schedule, nil
}

// ListSchedules returns all schedules
func (s *Scheduler) ListSchedules() []*Schedule {
	s.mu.RLock()
	defer s.mu.RUnlock()

	schedules := make([]*Schedule, 0, len(s.schedules))
	for _, schedule := range s.schedules {
		schedules = append(schedules, schedule)
	}

	return schedules
}

// EnableSchedule enables a schedule
func (s *Scheduler) EnableSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedule, exists := s.schedules[id]
	if !exists {
		return fmt.Errorf("schedule %s not found", id)
	}

	if schedule.Enabled {
		return nil // Already enabled
	}

	entryID, err := s.cron.AddFunc(schedule.CronExpr, func() {
		s.executeSchedule(id)
	})
	if err != nil {
		return fmt.Errorf("failed to enable schedule: %w", err)
	}

	s.entries[id] = entryID
	schedule.Enabled = true
	schedule.UpdatedAt = time.Now()

	return nil
}

// DisableSchedule disables a schedule
func (s *Scheduler) DisableSchedule(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	schedule, exists := s.schedules[id]
	if !exists {
		return fmt.Errorf("schedule %s not found", id)
	}

	if !schedule.Enabled {
		return nil // Already disabled
	}

	if entryID, exists := s.entries[id]; exists {
		s.cron.Remove(entryID)
		delete(s.entries, id)
	}

	schedule.Enabled = false
	schedule.UpdatedAt = time.Now()

	return nil
}

// RunNow executes a schedule immediately
func (s *Scheduler) RunNow(id string) error {
	go s.executeSchedule(id)
	return nil
}

func (s *Scheduler) executeSchedule(id string) {
	s.mu.Lock()
	schedule, exists := s.schedules[id]
	if !exists {
		s.mu.Unlock()
		return
	}

	schedule.LastRun = time.Now()
	schedule.RunCount++
	s.mu.Unlock()

	// Execute migration
	ctx := context.Background()
	err := s.executor.Execute(ctx, schedule)

	s.mu.Lock()
	defer s.mu.Unlock()

	if err != nil {
		schedule.FailCount++
	}

	// Update next run time
	cronSchedule, parseErr := cron.ParseStandard(schedule.CronExpr)
	if parseErr == nil {
		schedule.NextRun = cronSchedule.Next(time.Now())
	}
}

// GetStats returns scheduler statistics
type SchedulerStats struct {
	TotalSchedules    int       `json:"total_schedules"`
	ActiveSchedules   int       `json:"active_schedules"`
	DisabledSchedules int       `json:"disabled_schedules"`
	NextRun           time.Time `json:"next_run"`
}

func (s *Scheduler) GetStats() SchedulerStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	stats := SchedulerStats{
		TotalSchedules: len(s.schedules),
	}

	var nextRun time.Time
	for _, schedule := range s.schedules {
		if schedule.Enabled {
			stats.ActiveSchedules++
			if nextRun.IsZero() || schedule.NextRun.Before(nextRun) {
				nextRun = schedule.NextRun
			}
		} else {
			stats.DisabledSchedules++
		}
	}

	stats.NextRun = nextRun
	return stats
}
