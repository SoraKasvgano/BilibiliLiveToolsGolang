package httpapi

import (
	"context"
	"net/http"
	"strings"

	"bilibililivetools/gover/backend/service/auth"
	"bilibililivetools/gover/backend/store"
)

type contextKey string

const adminUserContextKey contextKey = "adminUser"

func AdminUserFromContext(ctx context.Context) *store.AdminUser {
	user, _ := ctx.Value(adminUserContextKey).(*store.AdminUser)
	return user
}

func AuthRequired(authSvc *auth.Service, apiBase string) func(http.Handler) http.Handler {
	authPrefix := apiBase + "/auth/login"
	statusPath := apiBase + "/auth/status"
	inboundPrefix := apiBase + "/integration/provider/inbound/"

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path

			// Skip auth for non-API paths (static files, swagger, etc.).
			if !strings.HasPrefix(path, apiBase+"/") {
				next.ServeHTTP(w, r)
				return
			}
			// Skip auth for login and status endpoints.
			if path == authPrefix || path == statusPath {
				next.ServeHTTP(w, r)
				return
			}
			// Skip auth for provider inbound webhooks (they have their own signature auth).
			if strings.HasPrefix(path, inboundPrefix) {
				next.ServeHTTP(w, r)
				return
			}

			bearerToken := ExtractToken(r)
			apiKeyToken := ExtractAPIAccessToken(r, bearerToken)
			if bearerToken == "" && apiKeyToken == "" {
				Error(w, -401, "unauthorized", http.StatusUnauthorized)
				return
			}

			if bearerToken != "" {
				user, err := authSvc.Validate(r.Context(), bearerToken)
				if err == nil && user != nil {
					ctx := context.WithValue(r.Context(), adminUserContextKey, user)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			ok, err := authSvc.ValidateAPIAccessToken(r.Context(), apiKeyToken)
			if err != nil || !ok {
				Error(w, -401, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ExtractToken(r *http.Request) string {
	// 1. Authorization: Bearer <token>
	if auth := r.Header.Get("Authorization"); auth != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(auth, prefix) {
			return strings.TrimSpace(auth[len(prefix):])
		}
	}
	// 2. Cookie fallback
	if cookie, err := r.Cookie("gover_session"); err == nil {
		return strings.TrimSpace(cookie.Value)
	}
	return ""
}

func ExtractAPIAccessToken(r *http.Request, fallback string) string {
	if raw := strings.TrimSpace(r.Header.Get("X-API-Key")); raw != "" {
		return raw
	}
	return strings.TrimSpace(fallback)
}
