//go:build e2e

package harness

import (
	"testing"
	"time"
)

// A timeout shorter than the interval must still probe again at the deadline, not give up after the first probe.
func TestPollProbesAtDeadline(t *testing.T) {
	start := time.Now()
	calls := 0
	ok := poll(80*time.Millisecond, time.Hour, func() bool {
		calls++
		return time.Since(start) >= 60*time.Millisecond
	})
	if !ok {
		t.Fatalf("condition became true before the deadline but poll gave up after %d probe(s)", calls)
	}
	if calls < 2 {
		t.Errorf("calls = %d, want at least 2", calls)
	}
}

func TestPollGivesUpAfterDeadline(t *testing.T) {
	start := time.Now()
	if poll(30*time.Millisecond, time.Hour, func() bool { return false }) {
		t.Fatal("poll reported success for a false condition")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("poll took %s, want about the 30ms timeout", elapsed)
	}
}
