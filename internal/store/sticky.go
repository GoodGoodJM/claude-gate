package store

import (
	"database/sql"
	"fmt"
	"time"
)

type StickySession struct {
	GateTokenID string    `json:"gate_token_id"`
	RealTokenID string    `json:"real_token_id"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s *Store) GetStickySession(gateTokenID string) (*StickySession, error) {
	row := s.readDB.QueryRow(
		`SELECT gate_token_id, real_token_id, expires_at
		 FROM sticky_sessions WHERE gate_token_id = ? AND expires_at > datetime('now')`,
		gateTokenID,
	)
	var ss StickySession
	err := row.Scan(&ss.GateTokenID, &ss.RealTokenID, &ss.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get sticky session: %w", err)
	}
	return &ss, nil
}

func (s *Store) UpsertStickySession(gateTokenID, realTokenID string, expiresAt time.Time) error {
	_, err := s.writeDB.Exec(
		`INSERT INTO sticky_sessions (gate_token_id, real_token_id, expires_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(gate_token_id) DO UPDATE SET real_token_id = excluded.real_token_id, expires_at = excluded.expires_at`,
		gateTokenID, realTokenID, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("upsert sticky session: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpiredStickySessions() (int64, error) {
	res, err := s.writeDB.Exec(`DELETE FROM sticky_sessions WHERE expires_at <= datetime('now')`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sticky sessions: %w", err)
	}
	return res.RowsAffected()
}

func (s *Store) DeleteStickySession(gateTokenID string) error {
	_, err := s.writeDB.Exec(`DELETE FROM sticky_sessions WHERE gate_token_id = ?`, gateTokenID)
	return err
}
