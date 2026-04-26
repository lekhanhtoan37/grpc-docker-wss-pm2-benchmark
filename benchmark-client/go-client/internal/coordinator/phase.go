package coordinator

import (
	"context"
	"sync"
	"time"
)

type PhaseManager struct {
	mu              sync.Mutex
	expectedWorkers int
	registrations   int
	phase           string
	measureStartMs  int64
	measureDuration int32
	warmupSeconds   int32

	allRegistered chan struct{}
	allResults    chan struct{}
}

func NewPhaseManager(expectedWorkers, warmupSeconds, measureSeconds int) *PhaseManager {
	return &PhaseManager{
		expectedWorkers: expectedWorkers,
		warmupSeconds:   int32(warmupSeconds),
		measureDuration: int32(measureSeconds),
		phase:           "waiting",
		allRegistered:   make(chan struct{}),
		allResults:      make(chan struct{}),
	}
}

func (pm *PhaseManager) ComputeBarrierTime() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	barrierMs := time.Now().Add(time.Duration(pm.warmupSeconds)*time.Second + 5*time.Second).UnixMilli()
	pm.measureStartMs = barrierMs
	pm.phase = "barrier_set"
}

func (pm *PhaseManager) RegisterWorker() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.registrations++
	if pm.registrations >= pm.expectedWorkers {
		select {
		case <-pm.allRegistered:
		default:
			close(pm.allRegistered)
		}
		return true
	}
	return false
}

func (pm *PhaseManager) WaitForWorkers(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-pm.allRegistered:
		pm.mu.Lock()
		pm.phase = "registered"
		pm.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (pm *PhaseManager) GetMeasureStartMs() int64 {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.measureStartMs
}

func (pm *PhaseManager) GetMeasureDuration() int32 {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.measureDuration
}

func (pm *PhaseManager) GetWarmupSeconds() int32 {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.warmupSeconds
}

func (pm *PhaseManager) RegistrationCount() int {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.registrations
}

func (pm *PhaseManager) RecordFinalResult() bool {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.registrations--
	if pm.registrations <= 0 {
		select {
		case <-pm.allResults:
		default:
			close(pm.allResults)
		}
		return true
	}
	return false
}

func (pm *PhaseManager) WaitForResults(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	select {
	case <-pm.allResults:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
