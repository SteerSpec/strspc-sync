//go:build e2e

package harness

import (
	"strings"
	"testing"
)

func TestTestSlug(t *testing.T) {
	for name, want := range map[string]string{
		"TestSyncForcedOpensPRs":            "syncforcedopensprs",
		"TestSyncUsesNonMainDefaultBranch":  "syncusesnonmaindefaultbranch",
		"TestConflictTier1Lifecycle/sub_a":  "conflicttier1lifecycle-sub-a",
		"TestMonitorDriftLifecycle":         "monitordriftlifecycle",
		"Test" + strings.Repeat("Long", 30): strings.Repeat("long", 10),
	} {
		if got := testSlug(name); got != want {
			t.Errorf("testSlug(%q) = %q, want %q", name, got, want)
		}
	}
}

// Per-test prefixes must differ between tests and stay under the run prefix that TestCleanupRun sweeps.
func TestPerTestPrefixesAreDistinctAndNested(t *testing.T) {
	base := "123-1"
	a := branchPrefix(base + "-" + testSlug("TestSyncForcedOpensPRs"))
	b := branchPrefix(base + "-" + testSlug("TestSyncRerunOpensNothing"))
	if a == b {
		t.Fatal("two tests share a branch prefix")
	}
	sweep := branchPrefix(base)
	for _, p := range []string{a, b} {
		if !strings.HasPrefix(p, sweep) {
			t.Errorf("%q is not under the cleanup-only prefix %q", p, sweep)
		}
	}
}
