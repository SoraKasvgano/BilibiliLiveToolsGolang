package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type maintenanceModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &maintenanceModule{deps: deps}
	})
}

func (m *maintenanceModule) Prefix() string {
	return m.deps.Config.APIBase + "/maintenance"
}

func (m *maintenanceModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/setting", Summary: "Get maintenance setting", Handler: m.getSetting},
		{Method: http.MethodPost, Pattern: "/setting", Summary: "Save maintenance setting", Handler: m.saveSetting},
		{Method: http.MethodGet, Pattern: "/status", Summary: "Get maintenance runtime status", Handler: m.status},
		{Method: http.MethodPost, Pattern: "/cleanup", Summary: "Queue cleanup job", Handler: m.cleanupNow},
		{Method: http.MethodPost, Pattern: "/vacuum", Summary: "Queue vacuum job", Handler: m.vacuumNow},
		{Method: http.MethodPost, Pattern: "/cancel", Summary: "Cancel current maintenance job", Handler: m.cancelCurrent},
	}
}

func (m *maintenanceModule) getSetting(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	setting, err := m.deps.Maintenance.GetSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, setting)
}

func (m *maintenanceModule) saveSetting(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	var req store.MaintenanceSetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := m.deps.Maintenance.SaveSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, updated)
}

func (m *maintenanceModule) status(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	status, err := m.deps.Maintenance.Status(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	historyLimit := parseIntAnyOrDefault(r.URL.Query().Get("historyLimit"), 40)
	if historyLimit < 0 {
		historyLimit = 0
	}
	typeFilter := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("type")))
	statusFilter := strings.TrimSpace(strings.ToLower(r.URL.Query().Get("status")))
	if historyLimit > 0 {
		filtered := make([]any, 0, historyLimit)
		for _, item := range status.History {
			if typeFilter != "" && strings.ToLower(string(item.Type)) != typeFilter {
				continue
			}
			if statusFilter != "" && strings.ToLower(item.Status) != statusFilter {
				continue
			}
			filtered = append(filtered, item)
			if len(filtered) >= historyLimit {
				break
			}
		}
		httpapi.OK(w, map[string]any{
			"running":     status.Running,
			"queueLength": status.QueueLength,
			"current":     status.Current,
			"history":     filtered,
			"setting":     status.Setting,
			"db":          status.DB,
		})
		return
	}
	httpapi.OK(w, status)
}

func (m *maintenanceModule) cleanupNow(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	var req struct {
		Days   int  `json:"days"`
		Vacuum bool `json:"vacuum"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Days <= 0 {
		if setting, err := m.deps.Maintenance.GetSetting(r.Context()); err == nil && setting.RetentionDays > 0 {
			req.Days = setting.RetentionDays
		} else {
			req.Days = 7
		}
	}
	jobID, err := m.deps.Maintenance.QueueCleanup(req.Days, req.Vacuum, "manual")
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"message": "queued",
		"jobId":   jobID,
		"days":    req.Days,
		"vacuum":  req.Vacuum,
	})
}

func (m *maintenanceModule) vacuumNow(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	jobID, err := m.deps.Maintenance.QueueVacuum("manual")
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"message": "queued",
		"jobId":   jobID,
	})
}

func (m *maintenanceModule) cancelCurrent(w http.ResponseWriter, r *http.Request) {
	if m.deps.Maintenance == nil {
		httpapi.Error(w, -1, "maintenance service not available", http.StatusOK)
		return
	}
	var req struct {
		JobID string `json:"jobId"`
	}
	if r.ContentLength > 0 {
		if err := httpapi.DecodeJSON(r, &req); err != nil {
			httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
			return
		}
	} else {
		req.JobID = strings.TrimSpace(r.URL.Query().Get("jobId"))
	}
	cancelled, err := m.deps.Maintenance.CancelCurrent(req.JobID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if !cancelled {
		httpapi.Error(w, 1, "no running maintenance job", http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]string{
		"jobId": strings.TrimSpace(req.JobID),
	})
}

func parseIntAnyOrDefault(raw string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return fallback
	}
	return value
}
