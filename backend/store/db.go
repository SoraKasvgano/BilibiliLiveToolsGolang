package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	dbPath string
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	db.SetConnMaxLifetime(20 * time.Minute)
	db.SetMaxIdleConns(4)
	db.SetMaxOpenConns(8)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}

	store := &Store{db: db, dbPath: path}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) DB() *sql.DB {
	return s.db
}

func (s *Store) DBPath() string {
	return s.dbPath
}

func (s *Store) WithTx(ctx context.Context, fn func(*sql.Tx) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) migrate(ctx context.Context) error {
	ddlStatements := make([]string, 0, len(schemaStatements))
	for _, stmt := range schemaStatements {
		if isPragmaStatement(stmt) {
			if _, err := s.db.ExecContext(ctx, stmt); err != nil {
				return err
			}
			continue
		}
		ddlStatements = append(ddlStatements, stmt)
	}

	if err := s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range ddlStatements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		for _, stmt := range seedStatements {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.ensureSchemaUpgrades(ctx)
}

func isPragmaStatement(stmt string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt)), "PRAGMA ")
}

func (s *Store) ensureSchemaUpgrades(ctx context.Context) error {
	if err := s.ensureColumn(ctx, "cookie_settings", "refresh_token", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "push_settings", "multi_input_enabled", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "push_settings", "multi_input_layout", "TEXT NOT NULL DEFAULT '2x2'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "push_settings", "multi_input_urls", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "push_settings", "multi_input_meta", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "danmaku_consumer_settings", "config_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, table string, column string, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer rows.Close()

	exists := false
	for rows.Next() {
		var (
			cid       int
			name      string
			colType   string
			notNull   int
			dfltValue sql.NullString
			pk        int
		)
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if name == column {
			exists = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = s.db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}
