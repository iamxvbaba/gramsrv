package memory

import (
	"context"
	"sync"
	"time"

	"telesrv/internal/domain"
)

type BootstrapUpdateJobStore struct {
	mu     sync.Mutex
	nextID int64
	jobs   map[int64]domain.BootstrapUpdateJob
	uniq   map[bootstrapUpdateJobKey]int64
}

type bootstrapUpdateJobKey struct {
	kind         domain.BootstrapUpdateJobKind
	userID       int64
	messageBoxID int
}

func NewBootstrapUpdateJobStore() *BootstrapUpdateJobStore {
	return &BootstrapUpdateJobStore{
		nextID: 1,
		jobs:   make(map[int64]domain.BootstrapUpdateJob),
		uniq:   make(map[bootstrapUpdateJobKey]int64),
	}
}

func (s *BootstrapUpdateJobStore) EnqueueLoginMessage(_ context.Context, job domain.BootstrapUpdateJob) (domain.BootstrapUpdateJob, error) {
	if job.Kind == "" {
		job.Kind = domain.BootstrapUpdateJobLoginMessage
	}
	if job.Status == "" {
		job.Status = domain.BootstrapUpdateJobPending
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	key := bootstrapUpdateJobKey{kind: job.Kind, userID: job.UserID, messageBoxID: job.MessageBoxID}
	if id, ok := s.uniq[key]; ok {
		existing := s.jobs[id]
		if existing.Status == domain.BootstrapUpdateJobFailed {
			existing.Status = domain.BootstrapUpdateJobPending
			existing.LastError = ""
		}
		existing.AuthKeyID = job.AuthKeyID
		existing.SessionID = job.SessionID
		existing.UpdatedAt = now
		s.jobs[id] = existing
		return existing, nil
	}
	job.ID = s.nextID
	s.nextID++
	job.CreatedAt = now
	job.UpdatedAt = now
	s.jobs[job.ID] = job
	s.uniq[key] = job.ID
	return job, nil
}

func (s *BootstrapUpdateJobStore) MarkReadyForSession(_ context.Context, userID int64, authKeyID [8]byte, sessionID int64) (int, error) {
	now := time.Now()
	count := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if job.UserID != userID || job.AuthKeyID != authKeyID || job.SessionID != sessionID || job.Status != domain.BootstrapUpdateJobPending {
			continue
		}
		job.Status = domain.BootstrapUpdateJobReady
		job.ReadyAt = now
		job.UpdatedAt = now
		s.jobs[id] = job
		count++
	}
	return count, nil
}

func (s *BootstrapUpdateJobStore) ClaimReady(_ context.Context, limit int, leaseTimeout time.Duration) ([]domain.BootstrapUpdateJob, error) {
	if limit <= 0 {
		limit = 100
	}
	if leaseTimeout <= 0 {
		leaseTimeout = 30 * time.Second
	}
	now := time.Now()
	out := make([]domain.BootstrapUpdateJob, 0, limit)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, job := range s.jobs {
		if len(out) >= limit {
			break
		}
		ready := job.Status == domain.BootstrapUpdateJobReady && (job.ReadyAt.IsZero() || !job.ReadyAt.After(now))
		stalePublishing := job.Status == domain.BootstrapUpdateJobPublishing && now.Sub(job.UpdatedAt) > leaseTimeout
		if !ready && !stalePublishing {
			continue
		}
		job.Status = domain.BootstrapUpdateJobPublishing
		job.Attempts++
		job.UpdatedAt = now
		s.jobs[id] = job
		out = append(out, job)
	}
	return out, nil
}

func (s *BootstrapUpdateJobStore) MarkPublished(_ context.Context, id int64) error {
	now := time.Now()
	s.mu.Lock()
	if job, ok := s.jobs[id]; ok {
		job.Status = domain.BootstrapUpdateJobPublished
		job.PublishedAt = now
		job.UpdatedAt = now
		s.jobs[id] = job
	}
	s.mu.Unlock()
	return nil
}

func (s *BootstrapUpdateJobStore) MarkFailed(_ context.Context, id int64, lastError string) error {
	now := time.Now()
	s.mu.Lock()
	if job, ok := s.jobs[id]; ok {
		if job.Attempts >= domain.BootstrapUpdateMaxAttempts {
			job.Status = domain.BootstrapUpdateJobFailed
		} else {
			job.Status = domain.BootstrapUpdateJobReady
			delay := time.Duration(job.Attempts*job.Attempts) * time.Second
			if delay > time.Minute {
				delay = time.Minute
			}
			job.ReadyAt = now.Add(delay)
		}
		job.LastError = lastError
		job.UpdatedAt = now
		s.jobs[id] = job
	}
	s.mu.Unlock()
	return nil
}
