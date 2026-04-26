package coordinator

import (
	"context"
	"testing"
	"time"
)

func TestNewPhaseManager(t *testing.T) {
	pm := NewPhaseManager(2, 30, 120)
	if pm.GetWarmupSeconds() != 30 {
		t.Errorf("warmup: got %d, want 30", pm.GetWarmupSeconds())
	}
	if pm.GetMeasureDuration() != 120 {
		t.Errorf("duration: got %d, want 120", pm.GetMeasureDuration())
	}
}

func TestPhaseManager_ComputeBarrierTime(t *testing.T) {
	pm := NewPhaseManager(2, 30, 120)
	before := time.Now().Add(35*time.Second).UnixMilli()
	pm.ComputeBarrierTime()
	after := time.Now().Add(35*time.Second).UnixMilli()

	barrier := pm.GetMeasureStartMs()
	if barrier < before || barrier > after {
		t.Errorf("barrier %d not in range [%d, %d]", barrier, before, after)
	}
}

func TestPhaseManager_RegisterAndWait(t *testing.T) {
	pm := NewPhaseManager(2, 5, 10)
	pm.ComputeBarrierTime()

	done := make(chan error, 1)
	go func() {
		done <- pm.WaitForWorkers(context.Background(), 5*time.Second)
	}()

	pm.RegisterWorker()
	time.Sleep(50 * time.Millisecond)

	if pm.RegistrationCount() != 1 {
		t.Errorf("after 1st register: count=%d, want 1", pm.RegistrationCount())
	}

	barrier := pm.GetMeasureStartMs()
	if barrier == 0 {
		t.Error("barrier should be non-zero after ComputeBarrierTime")
	}

	pm.RegisterWorker()

	if err := <-done; err != nil {
		t.Fatalf("WaitForWorkers failed: %v", err)
	}
}

func TestPhaseManager_WaitForWorkersTimeout(t *testing.T) {
	pm := NewPhaseManager(3, 5, 10)
	pm.ComputeBarrierTime()

	err := pm.WaitForWorkers(context.Background(), 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestPhaseManager_FinalResults(t *testing.T) {
	pm := NewPhaseManager(2, 5, 10)
	pm.ComputeBarrierTime()
	pm.RegisterWorker()
	pm.RegisterWorker()

	done := make(chan error, 1)
	go func() {
		done <- pm.WaitForResults(context.Background(), 5*time.Second)
	}()

	pm.RecordFinalResult()
	time.Sleep(50 * time.Millisecond)

	pm.RecordFinalResult()

	if err := <-done; err != nil {
		t.Fatalf("WaitForResults failed: %v", err)
	}
}

func TestPhaseManager_FinalResultsTimeout(t *testing.T) {
	pm := NewPhaseManager(2, 5, 10)
	pm.ComputeBarrierTime()
	pm.RegisterWorker()
	pm.RegisterWorker()

	err := pm.WaitForResults(context.Background(), 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout, got nil")
	}
}

func TestPhaseManager_BarrierPersistedAcrossRegisters(t *testing.T) {
	pm := NewPhaseManager(2, 30, 120)
	pm.ComputeBarrierTime()

	barrier1 := pm.GetMeasureStartMs()
	if barrier1 == 0 {
		t.Fatal("barrier should be non-zero after ComputeBarrierTime")
	}

	pm.RegisterWorker()
	barrier2 := pm.GetMeasureStartMs()
	if barrier2 != barrier1 {
		t.Errorf("barrier changed after register: was %d, now %d", barrier1, barrier2)
	}

	pm.RegisterWorker()
	barrier3 := pm.GetMeasureStartMs()
	if barrier3 != barrier1 {
		t.Errorf("barrier changed after 2nd register: was %d, now %d", barrier1, barrier3)
	}
}
