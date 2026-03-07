package state

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/SteerSpec/strspc-sync/internal/hash"
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
}

func NewDeploymentState() *DeploymentState {
	return &DeploymentState{
		Repositories: make(map[string]map[string]TemplateState),
		UpdatedAt:    time.Now(),
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

func (ds *DeploymentState) Save() ([]byte, error) {
	ds.UpdatedAt = time.Now()
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
