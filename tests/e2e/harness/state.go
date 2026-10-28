//go:build e2e

package harness

import (
	"testing"

	"github.com/SteerSpec/strspc-sync/internal/state"
)

// EmptyState is the deployment-state document ResetState writes.
const EmptyState = "{\"repositories\": {}}\n"

// ResetState writes an empty deployment state to .steerspec/deployment-state.json in the central repo, so the run
// starts with nothing recorded as deployed.
func (e *Env) ResetState(t *testing.T) {
	t.Helper()
	e.ResetFile(t, RepoCentral, StatePath, []byte(EmptyState))
}

// ReadState parses the deployment state currently stored in the central repo.
func (e *Env) ReadState(t *testing.T) *state.DeploymentState {
	t.Helper()
	ds, err := state.Load(e.ReadFile(t, RepoCentral, StatePath))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	return ds
}
