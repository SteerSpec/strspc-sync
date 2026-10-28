//go:build e2e

// Package e2e holds the end-to-end scenarios that drive the strspc binary against testbed-multi-org.
// Run them with `go test -tags e2e ./tests/e2e/...`; see tests/e2e/README.md.
package e2e

import (
	"testing"

	"github.com/SteerSpec/strspc-sync/tests/e2e/harness"
)

// TestCleanupRun removes whatever the run (E2E_RUN_ID) left in the testbed. The workflow runs it in an always() step
// after the scenarios, so an aborted or timed-out run still cleans up. It is a no-op when nothing was created and
// skips when the App secrets are unset.
func TestCleanupRun(t *testing.T) {
	harness.CleanupRun(t)
}
