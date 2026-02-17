package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	streamsvc "bilibililivetools/gover/backend/service/stream"
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
		{Method: http.MethodGet, Pattern: "/preview/mjpeg", Summary: "Preview current push source as MJPEG stream", Handler: m.preview},
		{Method: http.MethodPost, Pattern: "/preview/webrtc/offer", Summary: "Preview current push source via WebRTC (RTSP/H264)", Handler: m.previewWebRTCOffer},
		{Method: http.MethodPost, Pattern: "/preview/webrtc/close", Summary: "Close WebRTC preview session", Handler: m.previewWebRTCClose},
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

func (m *pushModule) preview(w http.ResponseWriter, r *http.Request) {
	setting, err := m.deps.Store.GetPushSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	var videoMaterial *store.Material
	if setting.VideoMaterialID != nil && *setting.VideoMaterialID > 0 {
		videoMaterial, err = m.deps.Store.GetMaterialByID(r.Context(), *setting.VideoMaterialID)
		if err != nil {
			httpapi.Error(w, -1, "video material not found: "+err.Error(), http.StatusOK)
			return
		}
	}
	command, args, err := streamsvc.BuildPreviewCommand(streamsvc.BuildContext{
		Setting:       setting,
		MediaDir:      m.deps.Config.MediaDir,
		VideoMaterial: videoMaterial,
		FFmpegPath:    m.deps.FFmpeg.BinaryPath(),
	}, previewOptionsFromRequest(r))
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if err := streamPreviewCommand(w, r, command, args, previewDebugEnabled(m.deps)); err != nil && r.Context().Err() == nil {
		log.Printf("[preview][push] stream failed: %v", err)
	}
}

func (m *pushModule) previewWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if m.deps.WebRTCPreview == nil {
		httpapi.Error(w, -1, "webrtc preview service not configured", http.StatusInternalServerError)
		return
	}
	var req struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	setting, err := m.deps.Store.GetPushSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	sourceURL, err := resolvePushWebRTCPreviewSource(setting)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	answer, err := m.deps.WebRTCPreview.StartRTSPPreview(r.Context(), sourceURL, req.SDP)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, answer)
}

func (m *pushModule) previewWebRTCClose(w http.ResponseWriter, r *http.Request) {
	if m.deps.WebRTCPreview == nil {
		httpapi.OKMessage(w, "webrtc preview service disabled")
		return
	}
	var req struct {
		SessionID string `json:"sessionId"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		httpapi.OKMessage(w, "empty session id, ignored")
		return
	}
	m.deps.WebRTCPreview.CloseSession(sessionID)
	httpapi.OKMessage(w, "closed")
}

func resolvePushWebRTCPreviewSource(setting *store.PushSetting) (string, error) {
	if setting == nil {
		return "", errors.New("missing push setting")
	}
	if setting.MultiInputEnabled {
		source := pickPrimaryRTSPFromMulti(setting.MultiInputMeta, setting.MultiInputURLs)
		if source != "" {
			return source, nil
		}
	}
	switch setting.InputType {
	case store.InputTypeRTSP, store.InputTypeONVIF:
		source := strings.TrimSpace(setting.RTSPURL)
		if !isRTSPSourceURL(source) {
			return "", errors.New("webrtc preview currently requires rtsp/rtsps input source")
		}
		return source, nil
	case store.InputTypeGB28181:
		source := strings.TrimSpace(setting.GBPullURL)
		if !isRTSPSourceURL(source) {
			return "", errors.New("gb28181 webrtc preview currently requires rtsp/rtsps pull url")
		}
		return source, nil
	default:
		return "", errors.New("webrtc preview currently supports rtsp/onvif/gb28181(rtsp) only")
	}
}

func pickPrimaryRTSPFromMulti(meta []store.MultiInputSource, urls []string) string {
	for _, item := range meta {
		value := strings.TrimSpace(item.URL)
		if value == "" || !isRTSPSourceURL(value) {
			continue
		}
		if item.Primary {
			return value
		}
	}
	for _, item := range meta {
		value := strings.TrimSpace(item.URL)
		if value != "" && isRTSPSourceURL(value) {
			return value
		}
	}
	for _, value := range urls {
		item := strings.TrimSpace(value)
		if item != "" && isRTSPSourceURL(item) {
			return item
		}
	}
	return ""
}

func isRTSPSourceURL(raw string) bool {
	value := strings.ToLower(strings.TrimSpace(raw))
	return strings.HasPrefix(value, "rtsp://") || strings.HasPrefix(value, "rtsps://")
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
