package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"bilibililivetools/gover/backend/store"
)

type Service struct {
	store         *store.Store
	sessionMaxAge time.Duration

	rateMu   sync.Mutex
	failures map[string]loginFailure
}

type loginFailure struct {
	count    int
	lockedAt time.Time
}

type LoginResult struct {
	Token     string   `json:"token"`
	ExpiresAt string   `json:"expiresAt"`
	User      UserInfo `json:"user"`
}

type UserInfo struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

const (
	maxLoginFailures = 5
	lockoutDuration  = 5 * time.Minute
	defaultMaxAge    = 24 * time.Hour
)

var defaultAPIAccessTokenKeyNames = []string{
	"api_access_token",
	"admin_api_token",
	"api_token",
	"bilibili",
}

func New(storeDB *store.Store, sessionMaxAge time.Duration) *Service {
	if sessionMaxAge <= 0 {
		sessionMaxAge = defaultMaxAge
	}
	return &Service{
		store:         storeDB,
		sessionMaxAge: sessionMaxAge,
		failures:      make(map[string]loginFailure),
	}
}

func (s *Service) Login(ctx context.Context, username string, password string) (*LoginResult, error) {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil, errors.New("username and password are required")
	}

	if s.isLockedOut(username) {
		return nil, errors.New("too many login attempts, please try again later")
	}

	user, err := s.store.GetAdminUserByUsername(ctx, username)
	if err != nil {
		s.recordFailure(username)
		return nil, errors.New("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		s.recordFailure(username)
		return nil, errors.New("invalid username or password")
	}

	s.clearFailure(username)

	token, err := generateToken()
	if err != nil {
		return nil, errors.New("failed to generate session token")
	}

	expiresAt := time.Now().UTC().Add(s.sessionMaxAge)
	if _, err := s.store.CreateAdminSession(ctx, user.ID, token, expiresAt); err != nil {
		return nil, err
	}

	// Best-effort cleanup of expired sessions.
	_, _ = s.store.DeleteExpiredAdminSessions(ctx)

	return &LoginResult{
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
		User: UserInfo{
			ID:       user.ID,
			Username: user.Username,
		},
	}, nil
}

func (s *Service) Validate(ctx context.Context, token string) (*store.AdminUser, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("empty token")
	}
	session, err := s.store.GetAdminSessionByToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, errors.New("invalid session")
	}
	if time.Now().UTC().After(session.ExpiresAt) {
		_ = s.store.DeleteAdminSession(ctx, token)
		return nil, errors.New("session expired")
	}
	user, err := s.store.GetAdminUserByID(ctx, session.UserID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Service) Logout(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return s.store.DeleteAdminSession(ctx, token)
}

func (s *Service) ChangePassword(ctx context.Context, userID int64, oldPassword string, newPassword string) error {
	oldPassword = strings.TrimSpace(oldPassword)
	newPassword = strings.TrimSpace(newPassword)
	if oldPassword == "" || newPassword == "" {
		return errors.New("old password and new password are required")
	}
	if len(newPassword) < 4 {
		return errors.New("new password must be at least 4 characters")
	}

	user, err := s.store.GetAdminUserByID(ctx, userID)
	if err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(oldPassword)); err != nil {
		return errors.New("old password is incorrect")
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 10)
	if err != nil {
		return errors.New("failed to hash new password")
	}

	if err := s.store.UpdateAdminUserPassword(ctx, userID, string(newHash)); err != nil {
		return err
	}

	// Invalidate all sessions for this user so they must re-login.
	return s.store.DeleteAdminSessionsByUserID(ctx, userID)
}

func (s *Service) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	return s.store.DeleteExpiredAdminSessions(ctx)
}

func (s *Service) ValidateAPIAccessToken(ctx context.Context, token string) (bool, error) {
	return s.store.VerifyAPIAccessToken(ctx, token, defaultAPIAccessTokenKeyNames)
}

func (s *Service) isLockedOut(username string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	f, ok := s.failures[username]
	if !ok {
		return false
	}
	if f.count >= maxLoginFailures && time.Since(f.lockedAt) < lockoutDuration {
		return true
	}
	if time.Since(f.lockedAt) >= lockoutDuration {
		delete(s.failures, username)
	}
	return false
}

func (s *Service) recordFailure(username string) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	f := s.failures[username]
	f.count++
	f.lockedAt = time.Now()
	s.failures[username] = f
}

func (s *Service) clearFailure(username string) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	delete(s.failures, username)
}

func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
