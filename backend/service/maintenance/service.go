package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bilibililivetools/gover/backend/store"
)

type JobType string

const (
	JobTypeCleanup JobType = "cleanup"
	JobTypeVacuum  JobType = "vacuum"
)

type JobStatus struct {
	ID            string              `json:"id"`
	Type          JobType             `json:"type"`
	Source        string              `json:"source"`
	RetentionDays int                 `json:"retentionDays"`
	WithVacuum    bool                `json:"withVacuum"`
	Status        string              `json:"status"`
	Message       string              `json:"message"`
	QueuedAt      time.Time           `json:"queuedAt"`
	StartedAt     *time.Time          `json:"startedAt,omitempty"`
	FinishedAt    *time.Time          `json:"finishedAt,omitempty"`
	DurationMS    int64               `json:"durationMs"`
	Progress      int                 `json:"progress"`
	Step          string              `json:"step"`
	Cleanup       *store.CleanupStats `json:"cleanup,omitempty"`
	BeforeDB      *store.DBStats      `json:"beforeDb,omitempty"`
	AfterDB       *store.DBStats      `json:"afterDb,omitempty"`
}

type Status struct {
	Running     bool                      `json:"running"`
	QueueLength int                       `json:"queueLength"`
	Current     *JobStatus                `json:"current,omitempty"`
	History     []JobStatus               `json:"history"`
	Setting     *store.MaintenanceSetting `json:"setting,omitempty"`
	DB          *store.DBStats            `json:"db"`
}

type queueRequest struct {
	id            string
	jobType       JobType
	source        string
	retentionDays int
	withVacuum    bool
	queuedAt      time.Time
}

type Service struct {
	store      *store.Store
	interval   time.Duration
	maxHistory int
	queue      chan queueRequest

	mu            sync.RWMutex
	cancel        context.CancelFunc
	current       *JobStatus
	currentCancel context.CancelFunc
	history       []JobStatus
	seq           uint64
}

func New(storeDB *store.Store) *Service {
	return &Service{
		store:      storeDB,
		interval:   30 * time.Minute,
		maxHistory: 40,
		queue:      make(chan queueRequest, 16),
		history:    make([]JobStatus, 0, 40),
	}
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	go s.workerLoop(ctx)
	go s.autoLoop(ctx)
}

func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	currentCancel := s.currentCancel
	s.cancel = nil
	s.currentCancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if currentCancel != nil {
		currentCancel()
	}
}

func (s *Service) GetSetting(ctx context.Context) (*store.MaintenanceSetting, error) {
	return s.store.GetMaintenanceSetting(ctx)
}

func (s *Service) SaveSetting(ctx context.Context, req store.MaintenanceSetting) (*store.MaintenanceSetting, error) {
	return s.store.SaveMaintenanceSetting(ctx, req)
}

func (s *Service) QueueCleanup(days int, withVacuum bool, source string) (string, error) {
	if days <= 0 {
		days = 7
	}
	if days > 3650 {
		days = 3650
	}
	id := s.nextJobID(JobTypeCleanup)
	req := queueRequest{
		id:            id,
		jobType:       JobTypeCleanup,
		source:        normalizedSource(source),
		retentionDays: days,
		withVacuum:    withVacuum,
		queuedAt:      time.Now(),
	}
	if err := s.enqueue(req); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) QueueVacuum(source string) (string, error) {
	id := s.nextJobID(JobTypeVacuum)
	req := queueRequest{
		id:       id,
		jobType:  JobTypeVacuum,
		source:   normalizedSource(source),
		queuedAt: time.Now(),
	}
	if err := s.enqueue(req); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Service) Status(ctx context.Context) (*Status, error) {
	s.mu.RLock()
	running := s.cancel != nil
	queueLength := len(s.queue)
	var current *JobStatus
	if s.current != nil {
		copied := *s.current
		current = &copied
	}
	history := make([]JobStatus, len(s.history))
	copy(history, s.history)
	s.mu.RUnlock()

	setting, err := s.store.GetMaintenanceSetting(ctx)
	if err != nil {
		return nil, err
	}
	db, err := s.store.DBStats(ctx)
	if err != nil {
		return nil, err
	}
	return &Status{
		Running:     running,
		QueueLength: queueLength,
		Current:     current,
		History:     history,
		Setting:     setting,
		DB:          &db,
	}, nil
}

func (s *Service) CancelCurrent(jobID string) (bool, error) {
	jobID = strings.TrimSpace(jobID)
	s.mu.RLock()
	current := s.current
	cancel := s.currentCancel
	s.mu.RUnlock()
	if current == nil || cancel == nil {
		return false, nil
	}
	if jobID != "" && !strings.EqualFold(jobID, current.ID) {
		return false, errors.New("job id does not match current running job")
	}
	cancel()
	return true, nil
}

func (s *Service) enqueue(req queueRequest) error {
	s.mu.RLock()
	running := s.cancel != nil
	s.mu.RUnlock()
	if !running {
		return errors.New("maintenance service not started")
	}
	select {
	case s.queue <- req:
		return nil
	default:
		return errors.New("maintenance queue is full")
	}
}

func (s *Service) workerLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-s.queue:
			s.runJob(req)
		}
	}
}

func (s *Service) autoLoop(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.maybeAutoCleanup(ctx)
		}
	}
}

func (s *Service) maybeAutoCleanup(ctx context.Context) {
	setting, err := s.store.GetMaintenanceSetting(ctx)
	if err != nil {
		log.Printf("[maintenance][warn] load setting failed: %v", err)
		return
	}
	if setting == nil || !setting.Enabled || setting.RetentionDays <= 0 {
		return
	}
	if setting.LastCleanupAt != nil && time.Since(*setting.LastCleanupAt) < 24*time.Hour {
		return
	}
	if s.hasRunningJob() {
		return
	}
	if _, err := s.QueueCleanup(setting.RetentionDays, setting.AutoVacuum, "auto"); err != nil {
		log.Printf("[maintenance][warn] queue auto cleanup failed: %v", err)
	}
}

func (s *Service) hasRunningJob() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.current != nil && s.current.Status == "running"
}

func (s *Service) runJob(req queueRequest) {
	now := time.Now()
	job := JobStatus{
		ID:            req.id,
		Type:          req.jobType,
		Source:        req.source,
		RetentionDays: req.retentionDays,
		WithVacuum:    req.withVacuum,
		Status:        "running",
		Message:       "running",
		QueuedAt:      req.queuedAt,
		StartedAt:     &now,
		Progress:      1,
		Step:          "queued",
	}
	beforeStats, _ := s.store.DBStats(context.Background())
	job.BeforeDB = &beforeStats
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	s.setCurrent(job, cancel)
	start := time.Now()

	var runErr error
	switch req.jobType {
	case JobTypeCleanup:
		runErr = s.runCleanupJob(ctx, req)
	case JobTypeVacuum:
		s.updateCurrentProgress(35, "vacuuming", "running sqlite vacuum")
		if err := s.store.Vacuum(ctx); err != nil {
			runErr = err
		} else {
			_ = s.store.MarkMaintenanceVacuum(context.Background(), time.Now())
		}
	default:
		runErr = errors.New("unsupported maintenance job type")
	}

	snapshot := s.snapshotCurrent()
	if snapshot != nil {
		job = *snapshot
	}
	afterStats, _ := s.store.DBStats(context.Background())
	job.AfterDB = &afterStats
	finished := time.Now()
	job.FinishedAt = &finished
	job.DurationMS = time.Since(start).Milliseconds()
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			job.Status = "cancelled"
			job.Message = runErr.Error()
			job.Progress = 100
			job.Step = "cancelled"
			log.Printf("[maintenance][warn] job=%s type=%s cancelled: %v", job.ID, job.Type, runErr)
		} else {
			job.Status = "failed"
			job.Message = runErr.Error()
			job.Progress = 100
			job.Step = "failed"
			log.Printf("[maintenance][error] job=%s type=%s failed: %v", job.ID, job.Type, runErr)
		}
	} else {
		job.Status = "succeeded"
		job.Message = "ok"
		job.Progress = 100
		job.Step = "finished"
		log.Printf("[maintenance] job=%s type=%s succeeded duration=%dms", job.ID, job.Type, job.DurationMS)
	}

	if payload, err := json.Marshal(job); err == nil {
		eventType := "maintenance.job.succeeded"
		if runErr != nil {
			if job.Status == "cancelled" {
				eventType = "maintenance.job.cancelled"
			} else {
				eventType = "maintenance.job.failed"
			}
		}
		_ = s.store.CreateLiveEvent(context.Background(), eventType, string(payload))
	}

	s.finishJob(job)
}

func (s *Service) runCleanupJob(ctx context.Context, req queueRequest) error {
	s.updateCurrentProgress(10, "prepare_cleanup", "starting cleanup")
	cutoff := time.Now().Add(-time.Duration(req.retentionDays) * 24 * time.Hour)
	s.updateCurrentProgress(45, "cleanup_data", "deleting old data in batches")
	cleanup, err := s.store.CleanupOldDataBefore(ctx, cutoff, 600)
	if err != nil {
		return err
	}
	_ = s.store.MarkMaintenanceCleanup(context.Background(), time.Now())
	s.updateCurrentCleanup(cleanup)
	s.updateCurrentProgress(75, "cleanup_done", "cleanup completed")
	if req.withVacuum {
		s.updateCurrentProgress(88, "vacuuming", "running sqlite vacuum")
		if err := s.store.Vacuum(ctx); err != nil {
			return fmt.Errorf("cleanup succeeded but vacuum failed: %w", err)
		}
		_ = s.store.MarkMaintenanceVacuum(context.Background(), time.Now())
	}
	s.updateCurrentProgress(96, "finishing", "collecting final stats")
	return nil
}

func (s *Service) setCurrent(job JobStatus, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = &job
	s.currentCancel = cancel
}

func (s *Service) finishJob(job JobStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current = nil
	s.currentCancel = nil
	s.history = append([]JobStatus{job}, s.history...)
	if len(s.history) > s.maxHistory {
		s.history = s.history[:s.maxHistory]
	}
}

func (s *Service) updateCurrentProgress(progress int, step string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return
	}
	if progress < 0 {
		progress = 0
	}
	if progress > 100 {
		progress = 100
	}
	s.current.Progress = progress
	if strings.TrimSpace(step) != "" {
		s.current.Step = strings.TrimSpace(step)
	}
	if strings.TrimSpace(message) != "" {
		s.current.Message = strings.TrimSpace(message)
	}
}

func (s *Service) updateCurrentCleanup(cleanup store.CleanupStats) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return
	}
	copyValue := cleanup
	s.current.Cleanup = &copyValue
}

func (s *Service) snapshotCurrent() *JobStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.current == nil {
		return nil
	}
	copied := *s.current
	return &copied
}

func (s *Service) nextJobID(jobType JobType) string {
	seq := atomic.AddUint64(&s.seq, 1)
	return fmt.Sprintf("%s-%d-%d", jobType, time.Now().UnixMilli(), seq)
}

func normalizedSource(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "manual"
	}
	return source
}
