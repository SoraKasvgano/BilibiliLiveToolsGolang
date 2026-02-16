package handlers

import (
	"net/http"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
)

type authModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &authModule{deps: deps}
	})
}

func (m *authModule) Prefix() string {
	return m.deps.Config.APIBase + "/auth"
}

func (m *authModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodPost, Pattern: "/login", Summary: "Admin login", Handler: m.login},
		{Method: http.MethodPost, Pattern: "/logout", Summary: "Admin logout", Handler: m.logout},
		{Method: http.MethodGet, Pattern: "/status", Summary: "Check auth status", Handler: m.status},
		{Method: http.MethodPost, Pattern: "/password", Summary: "Change admin password", Handler: m.changePassword},
	}
}

func (m *authModule) login(w http.ResponseWriter, r *http.Request) {
	if m.deps.Auth == nil {
		httpapi.Error(w, -1, "auth service not configured", http.StatusInternalServerError)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := m.deps.Auth.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *authModule) logout(w http.ResponseWriter, r *http.Request) {
	if m.deps.Auth == nil {
		httpapi.OKMessage(w, "ok")
		return
	}
	token := httpapi.ExtractToken(r)
	if token != "" {
		_ = m.deps.Auth.Logout(r.Context(), token)
	}
	httpapi.OKMessage(w, "logged out")
}

func (m *authModule) status(w http.ResponseWriter, r *http.Request) {
	if m.deps.Auth == nil {
		httpapi.OK(w, map[string]any{"authenticated": true, "user": nil})
		return
	}
	bearerToken := httpapi.ExtractToken(r)
	if bearerToken != "" {
		user, err := m.deps.Auth.Validate(r.Context(), bearerToken)
		if err == nil && user != nil {
			httpapi.OK(w, map[string]any{
				"authenticated": true,
				"mode":          "admin_session",
				"user": map[string]any{
					"id":       user.ID,
					"username": user.Username,
				},
			})
			return
		}
	}
	apiToken := httpapi.ExtractAPIAccessToken(r, bearerToken)
	ok, err := m.deps.Auth.ValidateAPIAccessToken(r.Context(), apiToken)
	if err == nil && ok {
		httpapi.OK(w, map[string]any{
			"authenticated": true,
			"mode":          "api_key",
		})
		return
	}
	httpapi.OK(w, map[string]any{"authenticated": false})
}

func (m *authModule) changePassword(w http.ResponseWriter, r *http.Request) {
	if m.deps.Auth == nil {
		httpapi.Error(w, -1, "auth service not configured", http.StatusInternalServerError)
		return
	}
	user := httpapi.AdminUserFromContext(r.Context())
	if user == nil {
		httpapi.Error(w, -401, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req struct {
		OldPassword string `json:"oldPassword"`
		NewPassword string `json:"newPassword"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.deps.Auth.ChangePassword(r.Context(), user.ID, req.OldPassword, req.NewPassword); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "password changed, all sessions invalidated")
}
