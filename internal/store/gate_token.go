package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"fmt"
	"strings"
	"time"
)

type GateToken struct {
	ID                string    `json:"id"`
	Token             string    `json:"token,omitempty"`
	Name              string    `json:"name"`
	IsActive          bool      `json:"is_active"`
	TotalInputTokens  int64     `json:"total_input_tokens"`
	TotalOutputTokens int64     `json:"total_output_tokens"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func generateGateToken() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)
	return "gate-" + strings.ToLower(encoded)
}

func (s *Store) CreateGateToken(name string) (*GateToken, error) {
	id := newID()
	token := generateGateToken()
	now := time.Now().UTC()

	_, err := s.writeDB.Exec(
		`INSERT INTO gate_tokens (id, token, name, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)`,
		id, token, name, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create gate token: %w", err)
	}

	return &GateToken{
		ID:        id,
		Token:     token,
		Name:      name,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (s *Store) GetGateToken(id string) (*GateToken, error) {
	row := s.readDB.QueryRow(
		`SELECT id, token, name, is_active, total_input_tokens, total_output_tokens, created_at, updated_at
		 FROM gate_tokens WHERE id = ?`, id,
	)
	return scanGateToken(row)
}

func (s *Store) GetGateTokenByToken(token string) (*GateToken, error) {
	row := s.readDB.QueryRow(
		`SELECT id, token, name, is_active, total_input_tokens, total_output_tokens, created_at, updated_at
		 FROM gate_tokens WHERE token = ?`, token,
	)
	return scanGateToken(row)
}

func (s *Store) ListGateTokens() ([]GateToken, error) {
	rows, err := s.readDB.Query(
		`SELECT id, token, name, is_active, total_input_tokens, total_output_tokens, created_at, updated_at
		 FROM gate_tokens ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("list gate tokens: %w", err)
	}
	defer rows.Close()

	var tokens []GateToken
	for rows.Next() {
		t, err := scanGateToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *t)
	}
	return tokens, rows.Err()
}

func (s *Store) UpdateGateToken(id, name string) error {
	res, err := s.writeDB.Exec(
		`UPDATE gate_tokens SET name = ?, updated_at = datetime('now') WHERE id = ?`,
		name, id,
	)
	if err != nil {
		return fmt.Errorf("update gate token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) SetGateTokenActive(id string, active bool) error {
	val := 0
	if active {
		val = 1
	}
	res, err := s.writeDB.Exec(
		`UPDATE gate_tokens SET is_active = ?, updated_at = datetime('now') WHERE id = ?`,
		val, id,
	)
	if err != nil {
		return fmt.Errorf("set gate token active: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteGateToken(id string) error {
	res, err := s.writeDB.Exec(`DELETE FROM gate_tokens WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete gate token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateGateTokenUsage(id string, inputTokens, outputTokens int64) error {
	_, err := s.writeDB.Exec(
		`UPDATE gate_tokens SET total_input_tokens = total_input_tokens + ?, total_output_tokens = total_output_tokens + ?, updated_at = datetime('now') WHERE id = ?`,
		inputTokens, outputTokens, id,
	)
	return err
}

func scanGateToken(scanner rowScanner) (*GateToken, error) {
	var t GateToken
	var isActive int
	err := scanner.Scan(
		&t.ID, &t.Token, &t.Name, &isActive,
		&t.TotalInputTokens, &t.TotalOutputTokens,
		&t.CreatedAt, &t.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	t.IsActive = isActive == 1
	return &t, nil
}
