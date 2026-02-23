package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store provides read/write access to the SQLite database.
// Uses separate connections for reads and writes for better concurrency with WAL mode.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

// New creates a new Store with the given database path.
// It initializes WAL mode and runs pending migrations.
func New(dbPath string) (*Store, error) {
	dsn := dbPath + "?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"

	writeDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open write db: %w", err)
	}
	writeDB.SetMaxOpenConns(1)

	readDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("open read db: %w", err)
	}
	readDB.SetMaxOpenConns(4)

	s := &Store{writeDB: writeDB, readDB: readDB}

	if err := s.migrate(); err != nil {
		s.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return s, nil
}

// Close closes both database connections.
func (s *Store) Close() error {
	return errors.Join(s.writeDB.Close(), s.readDB.Close())
}

// WriteDB returns the write-only database connection.
func (s *Store) WriteDB() *sql.DB {
	return s.writeDB
}

// ReadDB returns the read-only database connection.
func (s *Store) ReadDB() *sql.DB {
	return s.readDB
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (s *Store) withTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.writeDB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// cascadeDelete deletes related rows from sticky_sessions and usage_logs,
// then deletes the row from the target table. The column is the foreign key
// column name used in the WHERE clause (e.g. "real_token_id" or "gate_token_id").
func cascadeDelete(ctx context.Context, tx *sql.Tx, column, id, table string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sticky_sessions WHERE `+column+` = ?`, id); err != nil {
		return fmt.Errorf("delete %s: delete sticky sessions: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM usage_logs WHERE `+column+` = ?`, id); err != nil {
		return fmt.Errorf("delete %s: delete usage logs: %w", table, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete %s: %w", table, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete %s: rows affected: %w", table, err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) migrate() error {
	// Ensure schema_migrations table exists
	_, err := s.writeDB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	// Get applied versions
	applied := make(map[int]bool)
	rows, err := s.readDB.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return fmt.Errorf("query migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return fmt.Errorf("scan migration version: %w", err)
		}
		applied[v] = true
	}

	// Read migration files
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	type migration struct {
		version int
		name    string
	}

	var migrations []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		parts := strings.SplitN(e.Name(), "_", 2)
		if len(parts) < 2 {
			continue
		}
		v, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		migrations = append(migrations, migration{version: v, name: e.Name()})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})

	for _, m := range migrations {
		if applied[m.version] {
			continue
		}

		content, err := fs.ReadFile(migrationsFS, "migrations/"+m.name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", m.name, err)
		}

		tx, err := s.writeDB.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for migration %s: %w", m.name, err)
		}

		if _, err := tx.Exec(string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec migration %s: %w", m.name, err)
		}

		if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", m.version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", m.name, err)
		}
	}

	return nil
}
