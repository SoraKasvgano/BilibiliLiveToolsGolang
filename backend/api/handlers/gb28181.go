package handlers

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/service/gb28181"
	"bilibililivetools/gover/backend/store"
)

type gb28181Module struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &gb28181Module{deps: deps}
	})
}

func (m *gb28181Module) Prefix() string {
	return m.deps.Config.APIBase + "/gb28181"
}

func (m *gb28181Module) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/config", Summary: "Get GB28181 config", Handler: m.getConfig},
		{Method: http.MethodPost, Pattern: "/config", Summary: "Save GB28181 config", Handler: m.saveConfig},
		{Method: http.MethodPost, Pattern: "/start", Summary: "Start GB28181 service", Handler: m.start},
		{Method: http.MethodPost, Pattern: "/stop", Summary: "Stop GB28181 service", Handler: m.stop},
		{Method: http.MethodGet, Pattern: "/status", Summary: "Get GB28181 runtime status", Handler: m.status},
		{Method: http.MethodGet, Pattern: "/devices", Summary: "List GB28181 devices", Handler: m.listDevices},
		{Method: http.MethodGet, Pattern: "/devices/{id}", Summary: "Get GB28181 device detail", Handler: m.detailDevice},
		{Method: http.MethodPost, Pattern: "/devices/save", Summary: "Create or update GB28181 device", Handler: m.saveDevice},
		{Method: http.MethodPost, Pattern: "/devices/delete", Summary: "Delete GB28181 devices", Handler: m.deleteDevices},
		{Method: http.MethodGet, Pattern: "/devices/{id}/channels", Summary: "List channels of GB28181 device", Handler: m.listChannels},
		{Method: http.MethodPost, Pattern: "/devices/{id}/catalog/query", Summary: "Send catalog query to GB28181 device", Handler: m.queryCatalog},
		{Method: http.MethodPost, Pattern: "/invite", Summary: "Send INVITE to GB28181 device", Handler: m.invite},
		{Method: http.MethodPost, Pattern: "/bye", Summary: "Send BYE for GB28181 session", Handler: m.bye},
		{Method: http.MethodGet, Pattern: "/sessions", Summary: "List GB28181 sessions", Handler: m.sessions},
		{Method: http.MethodPost, Pattern: "/sessions/{callId}/reinvite", Summary: "Reinvite a GB28181 session", Handler: m.reinvite},
		{Method: http.MethodGet, Pattern: "/sessions/{callId}/sdp", Summary: "Export GB28181 session SDP file", Handler: m.exportSessionSDP},
		{Method: http.MethodPost, Pattern: "/sessions/{callId}/camera-source", Summary: "Create or update camera source from GB28181 session", Handler: m.bindSessionCameraSource},
	}
}

func (m *gb28181Module) service() *gb28181.Service {
	return m.deps.GB28181
}

func (m *gb28181Module) getConfig(w http.ResponseWriter, r *http.Request) {
	cfg := m.deps.Config
	if m.deps.ConfigMgr != nil {
		cfg = m.deps.ConfigMgr.Current()
	}
	httpapi.OK(w, map[string]any{
		"config": gbConfigPayload(cfg),
	})
}

func (m *gb28181Module) saveConfig(w http.ResponseWriter, r *http.Request) {
	if m.deps.ConfigMgr == nil {
		httpapi.Error(w, -1, "config manager not available", http.StatusOK)
		return
	}
	base := m.deps.ConfigMgr.Current()
	next, err := decodeGBConfigPatch(r, base)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := m.deps.ConfigMgr.Save(next)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"config": gbConfigPayload(saved),
	})
}

func (m *gb28181Module) start(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	if err := m.service().Start(r.Context()); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, m.service().Status())
}

func (m *gb28181Module) stop(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := m.service().Stop(ctx); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, m.service().Status())
}

func (m *gb28181Module) status(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	httpapi.OK(w, m.service().Status())
}

func (m *gb28181Module) listDevices(w http.ResponseWriter, r *http.Request) {
	result, err := m.deps.Store.ListGB28181Devices(r.Context(), store.GB28181DeviceListRequest{
		Keyword: strings.TrimSpace(r.URL.Query().Get("keyword")),
		Status:  strings.TrimSpace(r.URL.Query().Get("status")),
		Page:    parseIntOrDefault(r.URL.Query().Get("page"), 1),
		Limit:   parseIntOrDefault(r.URL.Query().Get("limit"), 20),
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) detailDevice(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid gb28181 device id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.GetGB28181DeviceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *gb28181Module) saveDevice(w http.ResponseWriter, r *http.Request) {
	var req store.GB28181DeviceSaveRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.SaveGB28181Device(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *gb28181Module) deleteDevices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	affected, err := m.deps.Store.DeleteGB28181Devices(r.Context(), req.IDs)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{"affected": affected})
}

func (m *gb28181Module) listChannels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid gb28181 device id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.GetGB28181DeviceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	result, err := m.deps.Store.ListGB28181ChannelsByDeviceID(r.Context(), item.DeviceID, parseIntOrDefault(r.URL.Query().Get("page"), 1), parseIntOrDefault(r.URL.Query().Get("limit"), 100))
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) queryCatalog(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid gb28181 device id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.GetGB28181DeviceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	result, err := m.service().QueryCatalog(r.Context(), item.DeviceID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) invite(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	var req gb28181.InviteRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := m.service().Invite(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) bye(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	var req struct {
		CallID string `json:"callId"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := m.service().Bye(r.Context(), req.CallID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) sessions(w http.ResponseWriter, r *http.Request) {
	items, err := m.deps.Store.ListGB28181Sessions(r.Context(), parseIntOrDefault(r.URL.Query().Get("limit"), 200), strings.TrimSpace(r.URL.Query().Get("status")))
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *gb28181Module) reinvite(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	callID := strings.TrimSpace(chi.URLParam(r, "callId"))
	if callID == "" {
		httpapi.Error(w, -1, "callId is required", http.StatusBadRequest)
		return
	}
	result, err := m.service().Reinvite(r.Context(), callID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *gb28181Module) exportSessionSDP(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	callID := strings.TrimSpace(chi.URLParam(r, "callId"))
	if callID == "" {
		httpapi.Error(w, -1, "callId is required", http.StatusBadRequest)
		return
	}
	result, err := m.service().ExportSessionSDP(r.Context(), callID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"session": result,
		"pullUrl": result.SDPPath,
	})
}

func (m *gb28181Module) bindSessionCameraSource(w http.ResponseWriter, r *http.Request) {
	if m.service() == nil {
		httpapi.Error(w, -1, "gb28181 service not available", http.StatusOK)
		return
	}
	callID := strings.TrimSpace(chi.URLParam(r, "callId"))
	if callID == "" {
		httpapi.Error(w, -1, "callId is required", http.StatusBadRequest)
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Enabled     *bool  `json:"enabled"`
		ApplyPush   bool   `json:"applyPush"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	sessionSDP, err := m.service().ExportSessionSDP(r.Context(), callID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}

	cameraID := int64(0)
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if current, findErr := m.deps.Store.FindCameraSourceByGBIdentity(r.Context(), sessionSDP.DeviceID, sessionSDP.ChannelID); findErr == nil {
		cameraID = current.ID
		if req.Enabled == nil {
			enabled = current.Enabled
		}
	} else if !errorsIsNoRows(findErr) {
		httpapi.Error(w, -1, findErr.Error(), http.StatusOK)
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "GB28181 " + sessionSDP.DeviceID + "-" + sessionSDP.ChannelID
	}
	camera, err := m.deps.Store.SaveCameraSource(r.Context(), store.CameraSourceSaveRequest{
		ID:          cameraID,
		Name:        name,
		SourceType:  string(store.CameraSourceTypeGB28181),
		GBPullURL:   sessionSDP.SDPPath,
		GBDeviceID:  sessionSDP.DeviceID,
		GBChannelID: sessionSDP.ChannelID,
		GBTransport: "udp",
		Description: strings.TrimSpace(req.Description),
		Enabled:     enabled,
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}

	resp := map[string]any{
		"session": sessionSDP,
		"camera":  camera,
	}
	if req.ApplyPush {
		pushSetting, getErr := m.deps.Store.GetPushSetting(r.Context())
		if getErr != nil {
			httpapi.Error(w, -1, getErr.Error(), http.StatusOK)
			return
		}
		updateReq := buildPushUpdateRequest(pushSetting)
		updateReq.InputType = string(store.InputTypeGB28181)
		updateReq.RTSPURL = ""
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = sessionSDP.SDPPath
		updated, updateErr := m.deps.Store.UpdatePushSetting(r.Context(), updateReq)
		if updateErr != nil {
			httpapi.Error(w, -1, updateErr.Error(), http.StatusOK)
			return
		}
		resp["pushSetting"] = updated
		resp["restartRequired"] = m.deps.Stream.Status() != store.PushStatusStopped
	}
	httpapi.OK(w, resp)
}

func errorsIsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

func decodeGBConfigPatch(r *http.Request, base config.Config) (config.Config, error) {
	var req map[string]any
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		return base, err
	}
	if value, ok := getBool(req, "enabled"); ok {
		base.GB28181Enabled = value
	}
	if value, ok := getBool(req, "gb28181Enabled"); ok {
		base.GB28181Enabled = value
	}
	if value, ok := getString(req, "listenIp"); ok {
		base.GB28181ListenIP = value
	}
	if value, ok := getString(req, "gb28181ListenIp"); ok {
		base.GB28181ListenIP = value
	}
	if value, ok := getInt(req, "listenPort"); ok {
		base.GB28181ListenPort = value
	}
	if value, ok := getInt(req, "gb28181ListenPort"); ok {
		base.GB28181ListenPort = value
	}
	if value, ok := getString(req, "transport"); ok {
		base.GB28181Transport = value
	}
	if value, ok := getString(req, "gb28181Transport"); ok {
		base.GB28181Transport = value
	}
	if value, ok := getString(req, "serverId"); ok {
		base.GB28181ServerID = value
	}
	if value, ok := getString(req, "gb28181ServerId"); ok {
		base.GB28181ServerID = value
	}
	if value, ok := getString(req, "realm"); ok {
		base.GB28181Realm = value
	}
	if value, ok := getString(req, "gb28181Realm"); ok {
		base.GB28181Realm = value
	}
	if value, ok := getString(req, "password"); ok {
		base.GB28181Password = value
	}
	if value, ok := getString(req, "gb28181Password"); ok {
		base.GB28181Password = value
	}
	if value, ok := getInt(req, "registerExpires"); ok {
		base.GB28181RegisterExpires = value
	}
	if value, ok := getInt(req, "gb28181RegisterExpires"); ok {
		base.GB28181RegisterExpires = value
	}
	if value, ok := getInt(req, "heartbeatInterval"); ok {
		base.GB28181HeartbeatInterval = value
	}
	if value, ok := getInt(req, "gb28181HeartbeatInterval"); ok {
		base.GB28181HeartbeatInterval = value
	}
	if value, ok := getString(req, "mediaIp"); ok {
		base.GB28181MediaIP = value
	}
	if value, ok := getString(req, "gb28181MediaIp"); ok {
		base.GB28181MediaIP = value
	}
	if value, ok := getInt(req, "mediaPort"); ok {
		base.GB28181MediaPort = value
	}
	if value, ok := getInt(req, "gb28181MediaPort"); ok {
		base.GB28181MediaPort = value
	}
	if value, ok := getInt(req, "mediaPortStart"); ok {
		base.GB28181MediaPortStart = value
	}
	if value, ok := getInt(req, "gb28181MediaPortStart"); ok {
		base.GB28181MediaPortStart = value
	}
	if value, ok := getInt(req, "mediaPortEnd"); ok {
		base.GB28181MediaPortEnd = value
	}
	if value, ok := getInt(req, "gb28181MediaPortEnd"); ok {
		base.GB28181MediaPortEnd = value
	}
	if value, ok := getInt(req, "ackTimeoutSec"); ok {
		base.GB28181AckTimeoutSec = value
	}
	if value, ok := getInt(req, "gb28181AckTimeoutSec"); ok {
		base.GB28181AckTimeoutSec = value
	}
	return base, nil
}

func gbConfigPayload(cfg config.Config) map[string]any {
	return map[string]any{
		"enabled":           cfg.GB28181Enabled,
		"listenIp":          cfg.GB28181ListenIP,
		"listenPort":        cfg.GB28181ListenPort,
		"transport":         cfg.GB28181Transport,
		"serverId":          cfg.GB28181ServerID,
		"realm":             cfg.GB28181Realm,
		"password":          cfg.GB28181Password,
		"registerExpires":   cfg.GB28181RegisterExpires,
		"heartbeatInterval": cfg.GB28181HeartbeatInterval,
		"mediaIp":           cfg.GB28181MediaIP,
		"mediaPort":         cfg.GB28181MediaPort,
		"mediaPortStart":    cfg.GB28181MediaPortStart,
		"mediaPortEnd":      cfg.GB28181MediaPortEnd,
		"ackTimeoutSec":     cfg.GB28181AckTimeoutSec,
	}
}
