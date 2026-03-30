package storage

// SkillScore holds the utility tracking data for a single skill.
type SkillScore struct {
	Name      string
	Score     int
	Uses      int
	Successes int
	Failures  int
	UpdatedAt string
}

// RecordSkillResult records a skill use outcome. On success the score is
// incremented; on failure it is decremented (floor 0).
func (s *Store) RecordSkillResult(skillName string, success bool) error {
	delta := 1
	if !success {
		delta = -1
	}

	_, err := s.db.Exec(`
		INSERT INTO skill_scores (name, score, uses, successes, failures, updated_at)
		VALUES (?, MAX(0, ?), 1, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(name) DO UPDATE SET
			score      = MAX(0, score + ?),
			uses       = uses + 1,
			successes  = successes + ?,
			failures   = failures + ?,
			updated_at = CURRENT_TIMESTAMP`,
		skillName, delta,
		boolToInt(success), boolToInt(!success),
		delta,
		boolToInt(success), boolToInt(!success),
	)
	return err
}

// GetSkillScores returns a map of skill name → current score for all tracked skills.
func (s *Store) GetSkillScores() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT name, score FROM skill_scores`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	scores := make(map[string]int)
	for rows.Next() {
		var name string
		var score int
		if err := rows.Scan(&name, &score); err != nil {
			return nil, err
		}
		scores[name] = score
	}
	return scores, rows.Err()
}

// ListSkillScores returns all skill score records ordered by score descending.
func (s *Store) ListSkillScores() ([]SkillScore, error) {
	rows, err := s.db.Query(`
		SELECT name, score, uses, successes, failures, updated_at
		FROM skill_scores
		ORDER BY score DESC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var out []SkillScore
	for rows.Next() {
		var ss SkillScore
		if err := rows.Scan(&ss.Name, &ss.Score, &ss.Uses, &ss.Successes, &ss.Failures, &ss.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
