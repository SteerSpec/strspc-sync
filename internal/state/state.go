package state

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/hash"
	"github.com/SteerSpec/strspc-sync/internal/template"
)

type DeploymentState struct {
	Repositories map[string]map[string]TemplateState `json:"repositories"`
	Hash         string                              `json:"hash"`
	UpdatedAt    time.Time                           `json:"updated_at"`
}

type TemplateState struct {
	Version   string    `json:"version"`
	Hash      string    `json:"hash"`
	Timestamp time.Time `json:"timestamp"`
	PRNumber  int       `json:"pr_number,omitempty"`
	PRStatus  string    `json:"pr_status,omitempty"`
	// PreviousVersion is the version deployed before Version was proposed:
	// the baseline a sync PR reports (SYNCPR-003). It survives forced re-runs
	// of the same open PR, which would otherwise overwrite the baseline with
	// the proposed version.
	PreviousVersion string `json:"previous_version,omitempty"`
	// Sections names the STEERSPEC:BEGIN/END sections the template manages, and SectionsHash is the hash of those
	// sections in the rendered file (template.SectionsHash). Both are recorded for marker-strategy templates only.
	// Drift checks compare SectionsHash instead of Hash, so content a repository adds outside those sections, and
	// sections other templates or the repository own, are not drift. Empty for other strategies and for records
	// written before they existed, which fall back to Hash.
	Sections     []string `json:"sections,omitempty"`
	SectionsHash string   `json:"sections_hash,omitempty"`
}

func NewDeploymentState() *DeploymentState {
	return &DeploymentState{
		Repositories: make(map[string]map[string]TemplateState),
		UpdatedAt:    time.Now().UTC(),
	}
}

func Load(data []byte) (*DeploymentState, error) {
	var ds DeploymentState
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	if ds.Repositories == nil {
		ds.Repositories = make(map[string]map[string]TemplateState)
	}
	return &ds, nil
}

// Fingerprint identifies the recorded deployments. It ignores UpdatedAt, which
// Save restamps on every call, so equal fingerprints mean nothing was recorded
// in between.
func (ds *DeploymentState) Fingerprint() (string, error) {
	data, err := json.Marshal(ds.Repositories)
	if err != nil {
		return "", fmt.Errorf("fingerprinting state: %w", err)
	}
	return hash.HashBytes(data), nil
}

func (ds *DeploymentState) Save() ([]byte, error) {
	ds.UpdatedAt = time.Now().UTC()
	ds.Hash = ds.computeHash()
	return json.MarshalIndent(ds, "", "  ")
}

func (ds *DeploymentState) GetTemplateState(repo, templateID string) *TemplateState {
	repoMap, ok := ds.Repositories[repo]
	if !ok {
		return nil
	}
	ts, ok := repoMap[templateID]
	if !ok {
		return nil
	}
	return &ts
}

func (ds *DeploymentState) SetTemplateState(repo, templateID string, ts TemplateState) {
	if ds.Repositories[repo] == nil {
		ds.Repositories[repo] = make(map[string]TemplateState)
	}
	ds.Repositories[repo][templateID] = ts
}

func (ds *DeploymentState) computeHash() string {
	data, _ := json.Marshal(ds.Repositories)
	return hash.HashBytes(data)
}

// ExpectedHash is the hash Drift compares a file against: SectionsHash when the record has one, otherwise Hash.
func (ts TemplateState) ExpectedHash() string {
	if ts.SectionsHash != "" && len(ts.Sections) > 0 {
		return ts.SectionsHash
	}
	return ts.Hash
}

// Drift compares content with what this record describes. With a SectionsHash only the template's own sections
// count; otherwise the whole file does. expected and actual are the hashes compared. actual is empty when the record
// has a SectionsHash and content lacks one of the sections or has a broken marker, which is drift: the managed
// part is gone or unreadable.
func (ts TemplateState) Drift(content []byte) (drifted bool, expected, actual string) {
	if ts.SectionsHash == "" || len(ts.Sections) == 0 {
		actual = hash.HashBytes(content)
		return actual != ts.Hash, ts.Hash, actual
	}
	actual, ok := template.SectionsHash(content, ts.Sections)
	if !ok {
		actual = ""
	}
	return actual != ts.SectionsHash, ts.SectionsHash, actual
}
