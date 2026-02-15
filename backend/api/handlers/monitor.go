package handlers

import (
	"io"
	"net/http"
	"net/mail"
	"strings"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type monitorModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &monitorModule{deps: deps}
	})
}

func (m *monitorModule) Prefix() string {
	return m.deps.Config.APIBase + "/monitor"
}

func (m *monitorModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "", Summary: "Get monitor settings", Handler: m.getSetting},
		{Method: http.MethodPost, Pattern: "/room", Summary: "Update monitor room settings", Handler: m.updateRoom},
		{Method: http.MethodPost, Pattern: "/email", Summary: "Update monitor email settings", Handler: m.updateEmail},
		{Method: http.MethodPost, Pattern: "/email/test", Summary: "Send monitor test email", Handler: m.testEmail},
		{Method: http.MethodGet, Pattern: "/status", Summary: "List monitor runtime logs", Handler: m.statusLogs},
	}
}

func (m *monitorModule) getSetting(w http.ResponseWriter, r *http.Request) {
	setting, err := m.deps.Store.GetMonitorSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, setting)
}

func (m *monitorModule) updateRoom(w http.ResponseWriter, r *http.Request) {
	var req store.MonitorRoomInfoUpdateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.RoomID <= 0 {
		httpapi.Error(w, -1, "roomId is required", http.StatusOK)
		return
	}
	if strings.TrimSpace(req.RoomURL) == "" {
		httpapi.Error(w, -1, "roomUrl is required", http.StatusOK)
		return
	}
	updated, err := m.deps.Store.UpdateMonitorRoomInfo(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if m.deps.Monitor != nil {
		m.deps.Monitor.Infof("monitor room setting updated enabled=%v roomId=%d", req.IsEnabled, req.RoomID)
	}
	httpapi.OK(w, updated)
}

func (m *monitorModule) updateEmail(w http.ResponseWriter, r *http.Request) {
	var req store.MonitorEmailUpdateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.SMTPServer) == "" {
		httpapi.Error(w, -1, "smtpServer is required", http.StatusOK)
		return
	}
	if req.SMTPPort <= 0 {
		httpapi.Error(w, -1, "smtpPort is required", http.StatusOK)
		return
	}
	if !isValidEmail(req.MailAddress) {
		httpapi.Error(w, -1, "mailAddress is invalid", http.StatusOK)
		return
	}
	for _, receiver := range normalizeReceivers(req.Receivers) {
		if !isValidEmail(receiver) {
			httpapi.Error(w, -1, "receiver is invalid: "+receiver, http.StatusOK)
			return
		}
	}
	updated, err := m.deps.Store.UpdateMonitorEmailInfo(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if m.deps.Monitor != nil {
		m.deps.Monitor.Infof("monitor email setting updated enabled=%v smtp=%s:%d",
			req.IsEnableEmailNotice, req.SMTPServer, req.SMTPPort)
	}
	httpapi.OK(w, updated)
}

func (m *monitorModule) testEmail(w http.ResponseWriter, r *http.Request) {
	if m.deps.Monitor == nil {
		httpapi.Error(w, -1, "monitor runtime is unavailable", http.StatusOK)
		return
	}
	var req struct {
		Subject   string   `json:"subject"`
		Body      string   `json:"body"`
		Receivers []string `json:"receivers"`
	}
	if r.ContentLength > 0 {
		if err := httpapi.DecodeJSON(r, &req); err != nil && err != io.EOF {
			httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
			return
		}
	}
	result, err := m.deps.Monitor.SendTestEmail(r.Context(), req.Subject, req.Body, req.Receivers)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"message": "test email sent",
		"result":  result,
	})
}

func (m *monitorModule) statusLogs(w http.ResponseWriter, r *http.Request) {
	if m.deps.Monitor == nil {
		httpapi.Error(w, -1, "monitor runtime is unavailable", http.StatusOK)
		return
	}
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	level := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("level")))
	logs := m.deps.Monitor.Logs(limit)
	if level == "" {
		httpapi.OK(w, map[string]any{
			"count": len(logs),
			"items": logs,
		})
		return
	}
	filtered := make([]any, 0, len(logs))
	for _, item := range logs {
		if strings.ToUpper(strings.TrimSpace(item.Level)) == level {
			filtered = append(filtered, item)
		}
	}
	httpapi.OK(w, map[string]any{
		"count": len(filtered),
		"items": filtered,
	})
}

func isValidEmail(email string) bool {
	email = strings.TrimSpace(email)
	if email == "" {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}

func normalizeReceivers(raw string) []string {
	raw = strings.ReplaceAll(raw, "；", ";")
	raw = strings.ReplaceAll(raw, "，", ";")
	raw = strings.ReplaceAll(raw, ",", ";")
	parts := strings.Split(raw, ";")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
