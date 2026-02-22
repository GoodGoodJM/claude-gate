package store

import (
	"fmt"
	"time"
)

type UsageLog struct {
	ID                       int64     `json:"id"`
	GateTokenID              string    `json:"gate_token_id"`
	RealTokenID              string    `json:"real_token_id"`
	Model                    string    `json:"model"`
	InputTokens              int64     `json:"input_tokens"`
	OutputTokens             int64     `json:"output_tokens"`
	CacheCreationInputTokens int64     `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64     `json:"cache_read_input_tokens"`
	RequestPath              string    `json:"request_path"`
	StatusCode               int       `json:"status_code"`
	CreatedAt                time.Time `json:"created_at"`
}

type UsageStats struct {
	TotalInputTokens         int64 `json:"total_input_tokens"`
	TotalOutputTokens        int64 `json:"total_output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	RequestCount             int64 `json:"request_count"`
}

func (s *Store) InsertUsageLog(log *UsageLog) error {
	_, err := s.writeDB.Exec(
		`INSERT INTO usage_logs (gate_token_id, real_token_id, model, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, request_path, status_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		log.GateTokenID, log.RealTokenID, log.Model,
		log.InputTokens, log.OutputTokens,
		log.CacheCreationInputTokens, log.CacheReadInputTokens,
		log.RequestPath, log.StatusCode,
	)
	if err != nil {
		return fmt.Errorf("insert usage log: %w", err)
	}
	return nil
}

func (s *Store) InsertUsageLogs(logs []UsageLog) error {
	tx, err := s.writeDB.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmt, err := tx.Prepare(
		`INSERT INTO usage_logs (gate_token_id, real_token_id, model, input_tokens, output_tokens, cache_creation_input_tokens, cache_read_input_tokens, request_path, status_code)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare stmt: %w", err)
	}
	defer stmt.Close()

	for _, log := range logs {
		_, err := stmt.Exec(
			log.GateTokenID, log.RealTokenID, log.Model,
			log.InputTokens, log.OutputTokens,
			log.CacheCreationInputTokens, log.CacheReadInputTokens,
			log.RequestPath, log.StatusCode,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec insert: %w", err)
		}
	}

	return tx.Commit()
}

func (s *Store) GetUsageStats(since time.Time) (*UsageStats, error) {
	row := s.readDB.QueryRow(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COUNT(*)
		 FROM usage_logs WHERE created_at >= ?`, since,
	)
	var stats UsageStats
	err := row.Scan(
		&stats.TotalInputTokens, &stats.TotalOutputTokens,
		&stats.CacheCreationInputTokens, &stats.CacheReadInputTokens,
		&stats.RequestCount,
	)
	if err != nil {
		return nil, fmt.Errorf("get usage stats: %w", err)
	}
	return &stats, nil
}

func (s *Store) GetUsageStatsByRealToken(realTokenID string, since time.Time) (*UsageStats, error) {
	row := s.readDB.QueryRow(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COUNT(*)
		 FROM usage_logs WHERE real_token_id = ? AND created_at >= ?`, realTokenID, since,
	)
	var stats UsageStats
	err := row.Scan(
		&stats.TotalInputTokens, &stats.TotalOutputTokens,
		&stats.CacheCreationInputTokens, &stats.CacheReadInputTokens,
		&stats.RequestCount,
	)
	if err != nil {
		return nil, fmt.Errorf("get usage stats by real token: %w", err)
	}
	return &stats, nil
}

func (s *Store) GetUsageStatsByGateToken(gateTokenID string, since time.Time) (*UsageStats, error) {
	row := s.readDB.QueryRow(
		`SELECT COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		        COALESCE(SUM(cache_creation_input_tokens), 0), COALESCE(SUM(cache_read_input_tokens), 0),
		        COUNT(*)
		 FROM usage_logs WHERE gate_token_id = ? AND created_at >= ?`, gateTokenID, since,
	)
	var stats UsageStats
	err := row.Scan(
		&stats.TotalInputTokens, &stats.TotalOutputTokens,
		&stats.CacheCreationInputTokens, &stats.CacheReadInputTokens,
		&stats.RequestCount,
	)
	if err != nil {
		return nil, fmt.Errorf("get usage stats by gate token: %w", err)
	}
	return &stats, nil
}

func (s *Store) ListUsageLogs(limit, offset int) ([]UsageLog, error) {
	rows, err := s.readDB.Query(
		`SELECT id, gate_token_id, real_token_id, model, input_tokens, output_tokens,
		        cache_creation_input_tokens, cache_read_input_tokens, request_path, status_code, created_at
		 FROM usage_logs ORDER BY created_at DESC LIMIT ? OFFSET ?`, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list usage logs: %w", err)
	}
	defer rows.Close()

	var logs []UsageLog
	for rows.Next() {
		var l UsageLog
		err := rows.Scan(
			&l.ID, &l.GateTokenID, &l.RealTokenID, &l.Model,
			&l.InputTokens, &l.OutputTokens,
			&l.CacheCreationInputTokens, &l.CacheReadInputTokens,
			&l.RequestPath, &l.StatusCode, &l.CreatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("scan usage log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
