package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
)

const realTokenColumns = "id, name, access_token, refresh_token, is_active, failure_count, last_failure_at, last_used_at, total_input_tokens, total_output_tokens, created_at, updated_at"

type RealToken struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	AccessToken       string     `json:"access_token,omitempty"`
	RefreshToken      string     `json:"refresh_token,omitempty"`
	IsActive          bool       `json:"is_active"`
	FailureCount      int        `json:"failure_count"`
	LastFailureAt     *time.Time `json:"last_failure_at,omitempty"`
	LastUsedAt        *time.Time `json:"last_used_at,omitempty"`
	TotalInputTokens  int64      `json:"total_input_tokens"`
	TotalOutputTokens int64      `json:"total_output_tokens"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) CreateRealToken(ctx context.Context, name, accessToken, refreshToken string) (*RealToken, error) {
	id := newID()
	now := time.Now().UTC()

	_, err := s.writeDB.ExecContext(ctx,
		`INSERT INTO real_tokens (id, name, access_token, refresh_token, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, accessToken, refreshToken, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create real token: %w", err)
	}

	return &RealToken{
		ID:           id,
		Name:         name,
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		IsActive:     true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

func (s *Store) GetRealToken(ctx context.Context, id string) (*RealToken, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+realTokenColumns+`
		 FROM real_tokens WHERE id = ?`, id,
	)
	return scanRealToken(row)
}

func (s *Store) ListRealTokens(ctx context.Context) ([]RealToken, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+realTokenColumns+`
		 FROM real_tokens ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list real tokens: %w", err)
	}
	defer rows.Close()

	var tokens []RealToken
	for rows.Next() {
		t, err := scanRealTokenRows(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

func (s *Store) ListActiveRealTokens(ctx context.Context, maxFailures int) ([]RealToken, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+realTokenColumns+`
		 FROM real_tokens WHERE is_active = 1 AND failure_count < ? ORDER BY created_at`,
		maxFailures,
	)
	if err != nil {
		return nil, fmt.Errorf("list active real tokens: %w", err)
	}
	defer rows.Close()

	var tokens []RealToken
	for rows.Next() {
		t, err := scanRealTokenRows(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

func (s *Store) UpdateRealToken(ctx context.Context, id, name string) error {
	res, err := s.writeDB.ExecContext(ctx,
		`UPDATE real_tokens SET name = ?, updated_at = datetime('now') WHERE id = ?`,
		name, id,
	)
	if err != nil {
		return fmt.Errorf("update real token: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update real token: rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetRealTokenActive(ctx context.Context, id string, active bool) error {
	val := 0
	if active {
		val = 1
	}
	res, err := s.writeDB.ExecContext(ctx,
		`UPDATE real_tokens SET is_active = ?, failure_count = CASE WHEN ? = 1 THEN 0 ELSE failure_count END, updated_at = datetime('now') WHERE id = ?`,
		val, val, id,
	)
	if err != nil {
		return fmt.Errorf("set real token active: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set real token active: rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteRealToken(ctx context.Context, id string) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete real token: begin tx: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM sticky_sessions WHERE real_token_id = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete real token: delete sticky sessions: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE real_token_id = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete real token: delete usage logs: %w", err)
	}

	res, err := tx.ExecContext(ctx, `DELETE FROM real_tokens WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete real token: %w", err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete real token: rows affected: %w", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return sql.ErrNoRows
	}

	return tx.Commit()
}

func (s *Store) IncrementRealTokenFailure(ctx context.Context, id string) error {
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE real_tokens SET failure_count = failure_count + 1, last_failure_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`,
		id,
	)
	return err
}

func (s *Store) UpdateRealTokenUsage(ctx context.Context, id string, inputTokens, outputTokens int64) error {
	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE real_tokens SET total_input_tokens = total_input_tokens + ?, total_output_tokens = total_output_tokens + ?, last_used_at = datetime('now'), updated_at = datetime('now') WHERE id = ?`,
		inputTokens, outputTokens, id,
	)
	return err
}

func (s *Store) GetRealTokenByAccessToken(ctx context.Context, accessToken string) (*RealToken, error) {
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+realTokenColumns+`
		 FROM real_tokens WHERE access_token = ?`, accessToken,
	)
	return scanRealToken(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRealToken(row rowScanner) (*RealToken, error) {
	var t RealToken
	var isActive int
	err := row.Scan(
		&t.ID, &t.Name, &t.AccessToken, &t.RefreshToken,
		&isActive, &t.FailureCount, &t.LastFailureAt, &t.LastUsedAt,
		&t.TotalInputTokens, &t.TotalOutputTokens, &t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.IsActive = isActive == 1
	return &t, nil
}

func scanRealTokenRows(rows *sql.Rows) (*RealToken, error) {
	return scanRealToken(rows)
}
