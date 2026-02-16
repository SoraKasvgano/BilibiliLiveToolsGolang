package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Store) GetAdminUserByUsername(ctx context.Context, username string) (*AdminUser, error) {
	const q = `SELECT id, username, password_hash, created_at, updated_at FROM admin_users WHERE username = ? LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, username)
	item := AdminUser{}
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.Username, &item.PasswordHash, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("user not found")
		}
		return nil, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) GetAdminUserByID(ctx context.Context, id int64) (*AdminUser, error) {
	const q = `SELECT id, username, password_hash, created_at, updated_at FROM admin_users WHERE id = ? LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, id)
	item := AdminUser{}
	var createdAt, updatedAt string
	if err := row.Scan(&item.ID, &item.Username, &item.PasswordHash, &createdAt, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("user not found")
		}
		return nil, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) UpdateAdminUserPassword(ctx context.Context, userID int64, newHash string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE admin_users SET password_hash=?, updated_at=? WHERE id=?`, newHash, now, userID)
	return err
}

func (s *Store) CreateAdminSession(ctx context.Context, userID int64, token string, expiresAt time.Time) (*AdminSession, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	exp := expiresAt.UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO admin_sessions (user_id, token, expires_at, created_at) VALUES (?, ?, ?, ?)`,
		userID, token, exp, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &AdminSession{
		ID:        id,
		UserID:    userID,
		Token:     token,
		ExpiresAt: expiresAt.UTC(),
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (s *Store) GetAdminSessionByToken(ctx context.Context, token string) (*AdminSession, error) {
	const q = `SELECT id, user_id, token, expires_at, created_at FROM admin_sessions WHERE token = ? LIMIT 1`
	row := s.db.QueryRowContext(ctx, q, token)
	item := AdminSession{}
	var expiresAt, createdAt string
	if err := row.Scan(&item.ID, &item.UserID, &item.Token, &expiresAt, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	item.ExpiresAt = parseSQLiteTime(expiresAt)
	item.CreatedAt = parseSQLiteTime(createdAt)
	return &item, nil
}

func (s *Store) DeleteAdminSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token = ?`, token)
	return err
}

func (s *Store) DeleteAdminSessionsByUserID(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE user_id = ?`, userID)
	return err
}

func (s *Store) DeleteExpiredAdminSessions(ctx context.Context) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE datetime(expires_at) < datetime(?)`, now)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) VerifyAPIAccessToken(ctx context.Context, token string, candidateNames []string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	checked := map[string]struct{}{}
	for _, rawName := range candidateNames {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		if _, ok := checked[name]; ok {
			continue
		}
		checked[name] = struct{}{}
		item, err := s.GetAPIKeyByName(ctx, name)
		if err != nil {
			return false, err
		}
		if item == nil {
			continue
		}
		expected := strings.TrimSpace(item.APIKey)
		if expected == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1 {
			return true, nil
		}
	}
	return false, nil
}
