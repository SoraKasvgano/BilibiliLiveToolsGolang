package handlers

import (
	"context"
	"net/http"
	"time"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type pushModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &pushModule{deps: deps}
	})
}

func (m *pushModule) Prefix() string {
	return m.deps.Config.APIBase + "/push"
}

func (m *pushModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/setting", Summary: "Get push setting", Handler: m.getSetting},
		{Method: http.MethodPost, Pattern: "/setting", Summary: "Update push setting", Handler: m.updateSetting},
		{Method: http.MethodPost, Pattern: "/start", Summary: "Start push stream", Handler: m.start},
		{Method: http.MethodPost, Pattern: "/stop", Summary: "Stop push stream", Handler: m.stop},
		{Method: http.MethodPost, Pattern: "/restart", Summary: "Restart push stream", Handler: m.restart},
		{Method: http.MethodGet, Pattern: "/status", Summary: "Get push status", Handler: m.status},
		{Method: http.MethodGet, Pattern: "/devices", Summary: "List available ffmpeg devices", Handler: m.devices},
		{Method: http.MethodGet, Pattern: "/codecs", Summary: "List available codecs", Handler: m.codecs},
		{Method: http.MethodGet, Pattern: "/version", Summary: "Get ffmpeg version", Handler: m.version},
	}
}

func (m *pushModule) getSetting(w http.ResponseWriter, r *http.Request) {
	setting, err := m.deps.Store.GetPushSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, setting)
}

func (m *pushModule) updateSetting(w http.ResponseWriter, r *http.Request) {
	var req store.PushSettingUpdateRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := m.deps.Store.UpdatePushSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if m.deps.Stream.Status() != store.PushStatusStopped {
		httpapi.Error(w, 1, "setting saved, restart required", http.StatusOK)
		return
	}
	httpapi.OK(w, updated)
}

func (m *pushModule) start(w http.ResponseWriter, r *http.Request) {
	if err := m.deps.Stream.Start(r.Context(), false); err != nil {
		httpapi.Error(w, -1, "start stream failed: "+err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *pushModule) stop(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := m.deps.Stream.Stop(ctx); err != nil {
		httpapi.Error(w, -1, "stop stream failed: "+err.Error(), http.StatusOK)
		return
	}
	if room, err := m.deps.Store.GetLiveSetting(r.Context()); err == nil && room.RoomID > 0 {
		_ = m.deps.Bilibili.StopLive(r.Context(), room.RoomID)
	}
	httpapi.OKMessage(w, "Success")
}

func (m *pushModule) restart(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := m.deps.Stream.Restart(ctx); err != nil {
		httpapi.Error(w, -1, "restart stream failed: "+err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *pushModule) status(w http.ResponseWriter, r *http.Request) {
	httpapi.OK(w, store.PushStatusResponse{Status: m.deps.Stream.Status()})
}

func (m *pushModule) devices(w http.ResponseWriter, r *http.Request) {
	video, audio, err := m.deps.FFmpeg.ListDevices(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	type payload struct {
		Video []store.DeviceInfo `json:"video"`
		Audio []store.DeviceInfo `json:"audio"`
	}
	httpapi.OK(w, payload{Video: video, Audio: audio})
}

func (m *pushModule) codecs(w http.ResponseWriter, r *http.Request) {
	codecs, err := m.deps.FFmpeg.ListVideoCodecs(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, codecs)
}

func (m *pushModule) version(w http.ResponseWriter, r *http.Request) {
	version, err := m.deps.FFmpeg.Version(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]string{"version": version})
}
