package storage

// Durable delivery for background results.
//
// Before this existed, a finished background answer lived only in a local
// variable in the goroutine that produced it: if the send failed, or the
// process restarted first, the answer was lost with no trace beyond one log
// line. The live journal shows that happening on 2026-08-13, twice on 08-14
// and again on 08-21 — including a correct, self-verified run the operator
// never saw.
//
// The ordering is the whole design: commit first, send second. Everything else
// here is bookkeeping around that one rule.

// outboxMaxAttempts caps redelivery. After this many failures the row stops
// being retried but stays visible as "failed" — a silent drop is what we are
// fixing, so giving up must never mean disappearing.
const outboxMaxAttempts = 5

// OutboxMessage is one undelivered (or undeliverable) reply.
type OutboxMessage struct {
	ID        int64
	ChatID    int64
	Text      string
	Origin    string
	Attempts  int
	LastError string
}

// EnqueueOutbox commits a reply before any send is attempted and returns its id.
func (s *Store) EnqueueOutbox(chatID int64, text, origin string) (int64, error) {
	res, err := s.db.Exec(`
		INSERT INTO outbox (chat_id, text, origin, state)
		VALUES (?, ?, ?, 'pending')
	`, chatID, text, origin)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// MarkOutboxDelivered records a successful send. Idempotent: re-marking an
// already delivered row changes nothing, so a racing retry cannot double-send.
func (s *Store) MarkOutboxDelivered(id int64, sentMessageID int64) error {
	_, err := s.db.Exec(`
		UPDATE outbox
		SET state = 'delivered', sent_message_id = ?, last_error = '', updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND state != 'delivered'
	`, sentMessageID, id)
	return err
}

// RecordOutboxFailure counts a failed attempt and retires the row once it has
// exhausted its attempts.
func (s *Store) RecordOutboxFailure(id int64, reason string) error {
	_, err := s.db.Exec(`
		UPDATE outbox
		SET attempts = attempts + 1,
		    last_error = ?,
		    state = CASE WHEN attempts + 1 >= ? THEN 'failed' ELSE state END,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND state = 'pending'
	`, reason, outboxMaxAttempts, id)
	return err
}

// PendingOutbox returns replies still owed to someone, oldest first.
func (s *Store) PendingOutbox(limit int) ([]OutboxMessage, error) {
	return s.outboxByState("pending", limit)
}

// FailedOutbox returns replies we gave up on. They are kept so an operator can
// see what never arrived instead of guessing.
func (s *Store) FailedOutbox(limit int) ([]OutboxMessage, error) {
	return s.outboxByState("failed", limit)
}

func (s *Store) outboxByState(state string, limit int) ([]OutboxMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, chat_id, text, origin, attempts, last_error
		FROM outbox
		WHERE state = ?
		ORDER BY id
		LIMIT ?
	`, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var out []OutboxMessage
	for rows.Next() {
		var m OutboxMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Text, &m.Origin, &m.Attempts, &m.LastError); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
