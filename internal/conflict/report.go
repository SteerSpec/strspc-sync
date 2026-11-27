package conflict

import "time"

// Severity indicates the severity level of a conflict.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// ConflictType identifies the kind of conflict detected.
type ConflictType string

const (
	TypeVersionDrift             ConflictType = "version-drift"
	TypeManualOverride           ConflictType = "manual-override"
	TypeCrossReferenceBroken     ConflictType = "cross-reference-broken"
	TypeDuplicateSkill           ConflictType = "duplicate-skill"
	TypeContradictoryInstruction ConflictType = "contradictory-instruction"
	TypeUnmanagedFile            ConflictType = "unmanaged-file"
	TypeSectionOverlap           ConflictType = "section-overlap"
)

// Valid reports whether t is one of the seven conflict types (CNFLENT-003).
func (t ConflictType) Valid() bool {
	switch t {
	case TypeVersionDrift, TypeManualOverride, TypeCrossReferenceBroken, TypeDuplicateSkill,
		TypeContradictoryInstruction, TypeUnmanagedFile, TypeSectionOverlap:
		return true
	}
	return false
}

// ConflictReport holds the results of a conflict scan.
type ConflictReport struct {
	ID              string          `json:"id"`
	Timestamp       time.Time       `json:"timestamp"`
	Entries         []ConflictEntry `json:"entries"`
	SeveritySummary SeveritySummary `json:"severity_summary"`
	// IssuesCreated and IssuesClosed count the conflict issues this run opened
	// and the ones it closed because their conflict no longer exists.
	IssuesCreated int `json:"issues_created"`
	IssuesClosed  int `json:"issues_closed"`
	// IssuesMoved counts the issues a run closed in a target repository because the central issue now tracks
	// their conflict (CNFLACT-006).
	IssuesMoved int `json:"issues_moved"`
	// IssuesRedacted counts the central issues this run created or updated as a stub, without details: the central
	// repository is public and the conflict concerns a private or internal repository. A conflict about one such
	// repository is described in an issue in that repository.
	IssuesRedacted int `json:"issues_redacted"`
}

// ConflictEntry describes a single detected conflict.
type ConflictEntry struct {
	Severity Severity     `json:"severity"`
	Type     ConflictType `json:"type"`
	// Repo is the repository the conflict concerns, in owner/repo format (CNFLENT-004). A cross-repository conflict
	// such as duplicate-skill concerns no single target: Repo is then the central repository its issue is filed in,
	// and Repos lists the participating repositories.
	Repo                string   `json:"repo"`
	Repos               []string `json:"repos,omitempty"`
	FilePath            string   `json:"file_path"`
	Description         string   `json:"description"`
	SuggestedResolution string   `json:"suggested_resolution,omitempty"`
}

// SeveritySummary counts entries by severity level.
type SeveritySummary struct {
	Critical int `json:"critical"`
	Warning  int `json:"warning"`
	Info     int `json:"info"`
}

// AddEntry appends a conflict entry to the report.
func (r *ConflictReport) AddEntry(entry ConflictEntry) {
	r.Entries = append(r.Entries, entry)
}

// ComputeSummary recalculates the severity summary from current entries.
func (r *ConflictReport) ComputeSummary() {
	r.SeveritySummary = SeveritySummary{}
	for _, e := range r.Entries {
		switch e.Severity {
		case SeverityCritical:
			r.SeveritySummary.Critical++
		case SeverityWarning:
			r.SeveritySummary.Warning++
		case SeverityInfo:
			r.SeveritySummary.Info++
		}
	}
}
