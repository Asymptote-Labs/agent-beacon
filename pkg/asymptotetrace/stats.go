package asymptotetrace

import (
	"sync"
	"sync/atomic"
)

type Stats struct {
	Accepted    uint64
	Dropped     uint64
	Written     uint64
	WriteErrors uint64
	LastError   string
}

type statsCounter struct {
	accepted    atomic.Uint64
	dropped     atomic.Uint64
	written     atomic.Uint64
	writeErrors atomic.Uint64

	mu        sync.Mutex
	lastError string
}

func (s *statsCounter) snapshot() Stats {
	s.mu.Lock()
	lastError := s.lastError
	s.mu.Unlock()

	return Stats{
		Accepted:    s.accepted.Load(),
		Dropped:     s.dropped.Load(),
		Written:     s.written.Load(),
		WriteErrors: s.writeErrors.Load(),
		LastError:   lastError,
	}
}

func (s *statsCounter) recordError(err error) {
	if err == nil {
		return
	}
	s.writeErrors.Add(1)
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
}
