package state

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	ds := NewDeploymentState()
	ds.SetTemplateState("org/repo1", "claude-md", TemplateState{
		Version:   "1.0",
		Hash:      "abc123",
		Timestamp: time.Now(),
		PRNumber:  42,
		PRStatus:  "merged",
	})

	data, err := ds.Save()
	if err != nil {
		t.Fatalf("save failed: %v", err)
	}

	loaded, err := Load(data)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}

	ts := loaded.GetTemplateState("org/repo1", "claude-md")
	if ts == nil {
		t.Fatal("expected template state, got nil")
	}
	if ts.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", ts.Version)
	}
	if ts.PRNumber != 42 {
		t.Errorf("expected PR number 42, got %d", ts.PRNumber)
	}
	if ts.PRStatus != "merged" {
		t.Errorf("expected PR status merged, got %s", ts.PRStatus)
	}
}

func TestHashChangesOnStateChange(t *testing.T) {
	ds := NewDeploymentState()
	ds.SetTemplateState("org/repo1", "t1", TemplateState{
		Version: "1.0",
		Hash:    "hash1",
	})
	data1, _ := ds.Save()

	loaded1, _ := Load(data1)
	hash1 := loaded1.Hash

	ds.SetTemplateState("org/repo1", "t1", TemplateState{
		Version: "2.0",
		Hash:    "hash2",
	})
	data2, _ := ds.Save()

	loaded2, _ := Load(data2)
	hash2 := loaded2.Hash

	if hash1 == hash2 {
		t.Error("expected hash to change when state changes")
	}
	if !strings.HasPrefix(hash1, "b3_") {
		t.Errorf("expected hash to start with b3_, got %s", hash1)
	}
}

func TestGetSetOperations(t *testing.T) {
	ds := NewDeploymentState()

	// Get on empty state returns nil
	if ts := ds.GetTemplateState("org/repo1", "t1"); ts != nil {
		t.Error("expected nil for nonexistent repo")
	}

	// Set and get
	ds.SetTemplateState("org/repo1", "t1", TemplateState{Version: "1.0"})
	ts := ds.GetTemplateState("org/repo1", "t1")
	if ts == nil {
		t.Fatal("expected template state, got nil")
	}
	if ts.Version != "1.0" {
		t.Errorf("expected version 1.0, got %s", ts.Version)
	}

	// Get nonexistent template in existing repo
	if ts := ds.GetTemplateState("org/repo1", "nonexistent"); ts != nil {
		t.Error("expected nil for nonexistent template")
	}
}

func TestConcurrentSetGet(t *testing.T) {
	ds := NewDeploymentState()
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			repo := fmt.Sprintf("org/repo-%d", n)
			mu.Lock()
			ds.SetTemplateState(repo, "tmpl", TemplateState{
				Version: fmt.Sprintf("v%d", n),
				Hash:    fmt.Sprintf("hash-%d", n),
			})
			mu.Unlock()

			mu.Lock()
			ts := ds.GetTemplateState(repo, "tmpl")
			mu.Unlock()
			if ts == nil {
				t.Errorf("goroutine %d: expected non-nil state", n)
			}
		}(i)
	}
	wg.Wait()

	// Verify all entries present
	for i := 0; i < 10; i++ {
		repo := fmt.Sprintf("org/repo-%d", i)
		ts := ds.GetTemplateState(repo, "tmpl")
		if ts == nil {
			t.Errorf("missing state for %s", repo)
		}
	}
}
