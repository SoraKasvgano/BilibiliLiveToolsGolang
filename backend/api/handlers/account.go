package handlers

import (
	"net/http"
	"strings"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
)

type accountModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &accountModule{deps: deps}
	})
}

func (m *accountModule) Prefix() string {
	return m.deps.Config.APIBase + "/account"
}

func (m *accountModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/status", Summary: "Get login status", Handler: m.status},
		{Method: http.MethodPost, Pattern: "/login/qrcode/start", Summary: "Start QR login", Handler: m.loginByQRCode},
		{Method: http.MethodPost, Pattern: "/logout", Summary: "Logout", Handler: m.logout},
		{Method: http.MethodGet, Pattern: "/cookie", Summary: "Get raw cookie", Handler: m.getCookie},
		{Method: http.MethodPost, Pattern: "/cookie", Summary: "Set raw cookie", Handler: m.setCookie},
		{Method: http.MethodGet, Pattern: "/cookie/need-refresh", Summary: "Check whether cookie needs refresh", Handler: m.needRefresh},
		{Method: http.MethodPost, Pattern: "/cookie/refresh", Summary: "Refresh cookie with refresh_token", Handler: m.refreshCookie},
		{Method: http.MethodPost, Pattern: "/stream-url", Summary: "Set fallback stream URL", Handler: m.setStreamURL},
	}
}

func (m *accountModule) status(w http.ResponseWriter, r *http.Request) {
	result, err := m.deps.Bilibili.GetLoginStatus(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *accountModule) loginByQRCode(w http.ResponseWriter, r *http.Request) {
	status, err := m.deps.Bilibili.RequestQRCodeLogin(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, status)
}

func (m *accountModule) logout(w http.ResponseWriter, r *http.Request) {
	if err := m.deps.Bilibili.Logout(r.Context()); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	_ = m.deps.Stream.Stop(r.Context())
	httpapi.OKMessage(w, "Success")
}

func (m *accountModule) getCookie(w http.ResponseWriter, r *http.Request) {
	cookie, err := m.deps.Store.GetCookieSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, cookie)
}

func (m *accountModule) setCookie(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content string `json:"content"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.deps.Bilibili.SetCookie(r.Context(), req.Content); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *accountModule) needRefresh(w http.ResponseWriter, r *http.Request) {
	need, err := m.deps.Bilibili.CookieNeedToRefresh(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]bool{"needRefresh": need})
}

func (m *accountModule) refreshCookie(w http.ResponseWriter, r *http.Request) {
	if err := m.deps.Bilibili.RefreshCookie(r.Context()); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *accountModule) setStreamURL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	m.deps.Bilibili.SetManualStreamURL(strings.TrimSpace(req.URL))
	httpapi.OKMessage(w, "Success")
}
