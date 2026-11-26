package sync

import (
	"context"
	"strings"
	"testing"

	gh "github.com/SteerSpec/strspc-sync/internal/github"
)

// SYNCOP-003: a run's trigger is push, manual or schedule. Empty means manual; anything else is refused before the
// run starts, so no Result ever carries another value.
func TestRunTrigger(t *testing.T) {
	for _, tc := range []struct {
		in      Trigger
		want    Trigger
		wantErr bool
	}{
		{TriggerPush, TriggerPush, false},
		{TriggerManual, TriggerManual, false},
		{TriggerSchedule, TriggerSchedule, false},
		{"", TriggerManual, false},
		{"pull_request", "", true},
		{"Push", "", true},
	} {
		repoSvc := newMockRepoService()
		repoSvc.repos["testorg"] = []*gh.Repository{
			{Owner: "testorg", Name: "a", FullName: "testorg/a", DefaultBranch: "main", Visibility: "public"},
		}
		syncer := New(makeTestConfig(), makeTestClient(repoSvc, newMockPRService()), "testorg", "central")
		result, err := syncer.Run(context.Background(), Options{Trigger: tc.in, DryRun: true})
		if tc.wantErr {
			if err == nil || !strings.Contains(err.Error(), "invalid trigger") || result != nil {
				t.Errorf("%q: got %+v, %v; want no result and an invalid trigger error", tc.in, result, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
			continue
		}
		if result.Trigger != tc.want {
			t.Errorf("%q: trigger %q, want %q", tc.in, result.Trigger, tc.want)
		}
	}
}

func TestTriggerValid(t *testing.T) {
	for _, tr := range []Trigger{TriggerPush, TriggerManual, TriggerSchedule} {
		if !tr.Valid() {
			t.Errorf("%q must be valid", tr)
		}
	}
	for _, tr := range []Trigger{"", "workflow_dispatch", "cron"} {
		if tr.Valid() {
			t.Errorf("%q must not be valid", tr)
		}
	}
}
