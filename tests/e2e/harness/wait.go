//go:build e2e

package harness

import (
	"testing"
	"time"
)

// pollInterval is the delay between Eventually probes; tests shorten it.
var pollInterval = time.Second

// Eventually polls fn every second until it returns true or timeout elapses, then fails t with msg. It absorbs
// Contents/Pulls API eventual consistency; it is not a substitute for asserting on the final state.
func Eventually(t *testing.T, timeout time.Duration, fn func() bool, msg string) {
	t.Helper()
	if !poll(timeout, pollInterval, fn) {
		t.Fatalf("not satisfied within %s: %s", timeout, msg)
	}
}

func poll(timeout, interval time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if fn() {
			return true
		}
		if !time.Now().Add(interval).Before(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}
