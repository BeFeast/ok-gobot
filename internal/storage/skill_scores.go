package storage

import "database/sql"

// SkillScore holds the persisted utility score for a single skill.
type SkillScore struct {
	SkillName string
	Score     int
	Uses      int
	Successes int
	UpdatedAt string
}

// RecordSkillOutcome records a skill use result. On success the score is
// incremented by 1; on failure it is decremented by 1 (floor: -10).
func (s *Store) RecordSkillOutcome(skillName string, success bool) error {
	delta := -1
	if success {
		delta = 1
	}
	succ := 0
	if success {
		succ = 1
	}
	_, err := s.db.Exec(`
		INSERT INTO skill_scores (skill_name, score, uses, successes, updated_at)
		VALUES (?, ?, 1, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(skill_name) DO UPDATE SET
			score      = MAX(score + ?, -10),
			uses       = uses + 1,
			successes  = successes + ?,
			updated_at = CURRENT_TIMESTAMP
	`, skillName, delta, succ, delta, succ)
	return err
}

// GetSkillScore returns the utility score for a skill (0 if never recorded).
func (s *Store) GetSkillScore(skillName string) (int, error) {
	var score int
	err := s.db.QueryRow(
		`SELECT score FROM skill_scores WHERE skill_name = ?`, skillName,
	).Scan(&score)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return score, err
}

// ListSkillScores returns all persisted skill scores ordered by score descending.
func (s *Store) ListSkillScores() ([]SkillScore, error) {
	rows, err := s.db.Query(`
		SELECT skill_name, score, uses, successes, updated_at
		FROM skill_scores
		ORDER BY score DESC, skill_name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var out []SkillScore
	for rows.Next() {
		var ss SkillScore
		if err := rows.Scan(&ss.SkillName, &ss.Score, &ss.Uses, &ss.Successes, &ss.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// ListSkillScoreMap returns skill scores as a map[skillName]score.
func (s *Store) ListSkillScoreMap() (map[string]int, error) {
	scores, err := s.ListSkillScores()
	if err != nil {
		return nil, err
	}
	m := make(map[string]int, len(scores))
	for _, ss := range scores {
		m[ss.SkillName] = ss.Score
	}
	return m, nil
}
