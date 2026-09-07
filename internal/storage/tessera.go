package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TesseraIntent is retained before network delivery. Payload and configuration
// bindings contain no credentials; failed/rejected records remain inspectable.
type TesseraIntent struct {
	Key, Fingerprint, Digest, OperationID, Payload, State, Receipt, LastError string
}

func (s *Store) migrateTessera() error {
	statements := []string{
		`ALTER TABLE outbox ADD COLUMN delivery_metadata TEXT NOT NULL DEFAULT '';`,
		`CREATE TABLE IF NOT EXISTS tessera_intents (upstream_key TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,digest TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE,payload TEXT NOT NULL,state TEXT NOT NULL DEFAULT 'pending',receipt TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',created_at DATETIME DEFAULT CURRENT_TIMESTAMP,updated_at DATETIME DEFAULT CURRENT_TIMESTAMP);`,
		`CREATE TABLE IF NOT EXISTS tessera_deliveries (event_key TEXT PRIMARY KEY,fingerprint TEXT NOT NULL,outbox_id INTEGER NOT NULL UNIQUE,chat_id INTEGER NOT NULL,topic_id INTEGER NOT NULL,sender_id INTEGER NOT NULL,target TEXT NOT NULL,sent_message_id INTEGER NOT NULL DEFAULT 0,FOREIGN KEY(outbox_id) REFERENCES outbox(id));`,
		`CREATE INDEX IF NOT EXISTS idx_tessera_delivered_message ON tessera_deliveries(chat_id,topic_id,sent_message_id);`,
	}
	for _, q := range statements {
		if _, err := s.db.Exec(q); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}
func (s *Store) RetainTesseraIntent(v TesseraIntent) (TesseraIntent, error) {
	if v.Key == "" || v.Fingerprint == "" || v.Digest == "" || v.OperationID == "" || v.Payload == "" {
		return TesseraIntent{}, errors.New("incomplete Tessera intent")
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO tessera_intents(upstream_key,fingerprint,digest,operation_id,payload) VALUES(?,?,?,?,?)`, v.Key, v.Fingerprint, v.Digest, v.OperationID, v.Payload)
	if err != nil {
		return TesseraIntent{}, err
	}
	old, err := s.TesseraIntent(v.Key)
	if err != nil {
		return TesseraIntent{}, err
	}
	if old.Fingerprint != v.Fingerprint || old.Digest != v.Digest {
		return old, errors.New("Tessera update conflicts with retained content, target, or connector configuration")
	}
	return old, nil
}
func (s *Store) TesseraIntent(key string) (TesseraIntent, error) {
	var v TesseraIntent
	err := s.db.QueryRow(`SELECT upstream_key,fingerprint,digest,operation_id,payload,state,receipt,last_error FROM tessera_intents WHERE upstream_key=?`, key).Scan(&v.Key, &v.Fingerprint, &v.Digest, &v.OperationID, &v.Payload, &v.State, &v.Receipt, &v.LastError)
	return v, err
}
func (s *Store) FinishTesseraIntent(key, op, receipt string) error {
	result, err := s.db.Exec(`UPDATE tessera_intents SET state='committed',receipt=?,last_error='',updated_at=CURRENT_TIMESTAMP WHERE upstream_key=? AND operation_id=? AND state!='committed'`, receipt, key, op)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		v, e := s.TesseraIntent(key)
		if e != nil {
			return e
		}
		if v.OperationID != op || v.State != "committed" {
			return errors.New("Tessera receipt does not match retained intent")
		}
	}
	return nil
}
func (s *Store) FailTesseraIntent(key, op, message string, rejected bool) error {
	state := "pending"
	if rejected {
		state = "rejected"
	}
	_, err := s.db.Exec(`UPDATE tessera_intents SET state=?,last_error=?,updated_at=CURRENT_TIMESTAMP WHERE upstream_key=? AND operation_id=? AND state!='committed'`, state, message, key, op)
	return err
}
func (s *Store) PendingTesseraIntents(fingerprint string) ([]TesseraIntent, error) {
	rows, err := s.db.Query(`SELECT upstream_key,fingerprint,digest,operation_id,payload,state,receipt,last_error FROM tessera_intents WHERE state='pending' AND fingerprint=? ORDER BY created_at,upstream_key`, fingerprint)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []TesseraIntent
	for rows.Next() {
		var v TesseraIntent
		if err = rows.Scan(&v.Key, &v.Fingerprint, &v.Digest, &v.OperationID, &v.Payload, &v.State, &v.Receipt, &v.LastError); err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// TesseraDelivery binds the Telegram message returned by a known successful send
// to its exact observed item. Unknown sends never invent a reply mapping.
type TesseraDelivery struct {
	EventKey, Fingerprint, Target, Metadata, Text      string
	OutboxID, ChatID, TopicID, SenderID, SentMessageID int64
}

func (s *Store) EnqueueTesseraDelivery(v TesseraDelivery) (int64, error) {
	if v.EventKey == "" || v.Fingerprint == "" || v.Target == "" || v.Metadata == "" {
		return 0, errors.New("incomplete Tessera attention delivery")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var id int64
	var fingerprint, target string
	err = tx.QueryRow(`SELECT outbox_id,fingerprint,target FROM tessera_deliveries WHERE event_key=?`, v.EventKey).Scan(&id, &fingerprint, &target)
	if err == nil {
		if fingerprint != v.Fingerprint || target != v.Target {
			return 0, errors.New("Tessera delivery conflicts with retained authority or target")
		}
		return id, nil
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	result, err := tx.Exec(`INSERT INTO outbox(chat_id,text,origin,state,delivery_metadata) VALUES(?,?,'tessera-attention','tessera_pending',?)`, v.ChatID, v.Text, v.Metadata)
	if err != nil {
		return 0, err
	}
	id, err = result.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = tx.Exec(`INSERT INTO tessera_deliveries(event_key,fingerprint,outbox_id,chat_id,topic_id,sender_id,target) VALUES(?,?,?,?,?,?,?)`, v.EventKey, v.Fingerprint, id, v.ChatID, v.TopicID, v.SenderID, v.Target)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}
func (s *Store) TesseraReplyBinding(fingerprint string, chatID, topicID, senderID, messageID int64) (TesseraDelivery, error) {
	if messageID <= 0 {
		return TesseraDelivery{}, errors.New("Tessera reply has no delivered message identity")
	}
	rows, err := s.db.Query(`SELECT event_key,fingerprint,outbox_id,chat_id,topic_id,sender_id,target,sent_message_id FROM tessera_deliveries WHERE chat_id=? AND topic_id=? AND sent_message_id=?`, chatID, topicID, messageID)
	if err != nil {
		return TesseraDelivery{}, err
	}
	defer rows.Close()
	var found []TesseraDelivery
	for rows.Next() {
		var v TesseraDelivery
		if err = rows.Scan(&v.EventKey, &v.Fingerprint, &v.OutboxID, &v.ChatID, &v.TopicID, &v.SenderID, &v.Target, &v.SentMessageID); err != nil {
			return v, err
		}
		found = append(found, v)
	}
	if err = rows.Err(); err != nil {
		return TesseraDelivery{}, err
	}
	if len(found) == 0 {
		return TesseraDelivery{}, sql.ErrNoRows
	}
	if len(found) != 1 {
		return TesseraDelivery{}, errors.New("ambiguous Tessera reply binding")
	}
	v := found[0]
	if v.Fingerprint != fingerprint || v.SenderID != senderID {
		return v, fmt.Errorf("Tessera reply belongs to another sender or connector configuration")
	}
	return v, nil
}

// ReclaimStaleTesseraOutbox also runs periodically. A quick restart can leave a
// fresh prior-process claim ineligible at startup; it must become retryable later.
func (s *Store) ReclaimStaleTesseraOutbox() (int64, error) {
	result, err := s.db.Exec(`UPDATE outbox SET state='tessera_pending',updated_at=CURRENT_TIMESTAMP WHERE state='tessera_sending' AND updated_at<=datetime('now','-5 minutes')`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// RetryTesseraOutbox grants one explicit attempt to exhausted deliveries for the
// same authority and route. Preserve the lifetime attempt count/duplicate notice.
func (s *Store) RetryTesseraOutbox(fingerprint string, chat, topic, sender int64) (int64, error) {
	result, err := s.db.Exec(`UPDATE outbox SET state='tessera_pending',updated_at=CURRENT_TIMESTAMP WHERE state='tessera_failed' AND id IN (SELECT outbox_id FROM tessera_deliveries WHERE fingerprint=? AND chat_id=? AND topic_id=? AND sender_id=?)`, fingerprint, chat, topic, sender)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
