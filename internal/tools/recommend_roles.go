package tools

import (
	"context"
	"fmt"

	"ok-gobot/internal/recommend"
)

// RecommendRolesTool analyzes chat and job patterns and proposes new agent
// roles with schedules and output examples.
type RecommendRolesTool struct {
	recommender *recommend.Recommender
}

// NewRecommendRolesTool creates a recommend_roles tool backed by the given store.
func NewRecommendRolesTool(store recommend.PatternStore) *RecommendRolesTool {
	return &RecommendRolesTool{
		recommender: recommend.New(store),
	}
}

func (t *RecommendRolesTool) Name() string {
	return "recommend_roles"
}

func (t *RecommendRolesTool) Description() string {
	return "Analyze chat and job patterns to recommend new agent roles with schedules and output examples."
}

func (t *RecommendRolesTool) GetSchema() map[string]interface{} {
	return map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
	}
}

func (t *RecommendRolesTool) Execute(ctx context.Context, args ...string) (string, error) {
	if t.recommender == nil {
		return "", fmt.Errorf("recommender is not configured")
	}

	recs, err := t.recommender.Analyze()
	if err != nil {
		return "", fmt.Errorf("analysis failed: %w", err)
	}

	return recommend.Format(recs), nil
}

// ExecuteJSON: the tool takes no parameters; accept and ignore any
// (tool-schema disease, fixed fleet-wide 2026-08-21).
func (t *RecommendRolesTool) ExecuteJSON(ctx context.Context, params map[string]string) (string, error) {
	return t.Execute(ctx)
}
