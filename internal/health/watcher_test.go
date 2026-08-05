package health

import (
	"testing"
	"time"
)

const testWindow = time.Hour

// A container that keeps failing inside the window must trip the breaker.
func TestRestartCountTripsBreakerWithinWindow(t *testing.T) {
	state := &containerState{}
	now := time.Now()

	for i := 1; i <= MaxRestartAttempts; i++ {
		if got := state.recordRestart(now.Add(time.Duration(i)*time.Minute), testWindow); got != i {
			t.Fatalf("restart %d: got count %d, want %d", i, got, i)
		}
	}

	if got := state.restartCount(now.Add(6*time.Minute), testWindow); got < MaxRestartAttempts {
		t.Fatalf("breaker should have tripped: got %d, want >= %d", got, MaxRestartAttempts)
	}
}

// Restarts older than the window must age out so a container that fails once a
// day is not treated as flapping.
func TestRestartCountAgesOutOfWindow(t *testing.T) {
	state := &containerState{}
	now := time.Now()

	state.recordRestart(now.Add(-90*time.Minute), testWindow)
	state.recordRestart(now.Add(-80*time.Minute), testWindow)
	state.recordRestart(now.Add(-10*time.Minute), testWindow)

	if got := state.restartCount(now, testWindow); got != 1 {
		t.Fatalf("got %d in-window restarts, want 1", got)
	}
}

// Regression: the original code reset the counter whenever the container
// reported healthy, so unhealthy -> restart -> healthy -> unhealthy never
// accumulated and the breaker never tripped. Recovery must not erase history.
func TestRecoveryDoesNotEraseRestartHistory(t *testing.T) {
	state := &containerState{}
	now := time.Now()

	for i := range MaxRestartAttempts {
		// Each cycle: a restart, then the container reports healthy again.
		state.recordRestart(now.Add(time.Duration(i)*time.Minute), testWindow)
		state.consecutiveUnhealthy = 0
		state.observed = false
	}

	if got := state.restartCount(now.Add(time.Duration(MaxRestartAttempts)*time.Minute), testWindow); got != MaxRestartAttempts {
		t.Fatalf("flapping container: got %d restarts, want %d — recovery must not reset the window", got, MaxRestartAttempts)
	}
}

// A new image clears the history so a genuine fix gets a fresh budget.
func TestNewImageClearsRestartHistory(t *testing.T) {
	state := &containerState{}
	now := time.Now()

	state.recordRestart(now, testWindow)
	state.recordRestart(now, testWindow)
	state.restarts = nil // what processContainer does on an image change

	if got := state.restartCount(now, testWindow); got != 0 {
		t.Fatalf("got %d restarts after image change, want 0", got)
	}
}
