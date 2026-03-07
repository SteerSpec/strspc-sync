package conflict

import (
	"context"
	"fmt"
)

func (s *Scanner) runTier3(ctx context.Context, targets []string) ([]ConflictEntry, error) {
	// Tier 3 (LLM-assisted) is a placeholder for future implementation.
	// Per spec CNFLDTS-004/005, this must be explicitly opted into.
	return nil, fmt.Errorf("tier 3 (LLM-assisted) conflict detection is not yet implemented")
}
