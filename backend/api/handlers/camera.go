package handlers

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	streamsvc "bilibililivetools/gover/backend/service/stream"
	"bilibililivetools/gover/backend/store"
)

type cameraModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &cameraModule{deps: deps}
	})
}

func (m *cameraModule) Prefix() string {
	return m.deps.Config.APIBase + "/cameras"
}

func (m *cameraModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "", Summary: "List camera sources", Handler: m.list},
		{Method: http.MethodGet, Pattern: "/{id}", Summary: "Get camera source detail", Handler: m.detail},
		{Method: http.MethodPost, Pattern: "/save", Summary: "Create or update camera source", Handler: m.save},
		{Method: http.MethodPost, Pattern: "/delete", Summary: "Delete camera sources", Handler: m.delete},
		{Method: http.MethodPost, Pattern: "/{id}/apply-push", Summary: "Apply camera source to push setting", Handler: m.applyPush},
		{Method: http.MethodGet, Pattern: "/{id}/preview/mjpeg", Summary: "Preview selected camera source as MJPEG stream", Handler: m.preview},
		{Method: http.MethodPost, Pattern: "/{id}/preview/webrtc/offer", Summary: "Preview selected camera source via WebRTC (RTSP/H264)", Handler: m.previewWebRTCOffer},
	}
}

func (m *cameraModule) list(w http.ResponseWriter, r *http.Request) {
	sourceType := strings.TrimSpace(r.URL.Query().Get("sourceType"))
	if sourceType == "" {
		sourceType = strings.TrimSpace(r.URL.Query().Get("type"))
	}
	result, err := m.deps.Store.ListCameraSources(r.Context(), store.CameraSourceListRequest{
		Keyword:    strings.TrimSpace(r.URL.Query().Get("keyword")),
		SourceType: sourceType,
		Page:       parseIntOrDefault(r.URL.Query().Get("page"), 1),
		Limit:      parseIntOrDefault(r.URL.Query().Get("limit"), 20),
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *cameraModule) detail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid camera source id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Store.GetCameraSourceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *cameraModule) save(w http.ResponseWriter, r *http.Request) {
	var raw struct {
		ID                  int64  `json:"id"`
		Name                string `json:"name"`
		SourceType          string `json:"sourceType"`
		RTSPURL             string `json:"rtspUrl"`
		MJPEGURL            string `json:"mjpegUrl"`
		RTMPURL             string `json:"rtmpUrl"`
		GBPullURL           string `json:"gbPullUrl"`
		GBDeviceID          string `json:"gbDeviceId"`
		GBChannelID         string `json:"gbChannelId"`
		GBServer            string `json:"gbServer"`
		GBTransport         string `json:"gbTransport"`
		ONVIFEndpoint       string `json:"onvifEndpoint"`
		ONVIFUsername       string `json:"onvifUsername"`
		ONVIFPassword       string `json:"onvifPassword"`
		ONVIFProfileToken   string `json:"onvifProfileToken"`
		USBDeviceName       string `json:"usbDeviceName"`
		USBDeviceResolution string `json:"usbDeviceResolution"`
		USBDeviceFramerate  int    `json:"usbDeviceFramerate"`
		Description         string `json:"description"`
		Enabled             *bool  `json:"enabled"`
	}
	if err := httpapi.DecodeJSON(r, &raw); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}

	enabled := true
	if raw.Enabled != nil {
		enabled = *raw.Enabled
	} else if raw.ID > 0 {
		if current, err := m.deps.Store.GetCameraSourceByID(r.Context(), raw.ID); err == nil {
			enabled = current.Enabled
		}
	}

	saved, err := m.deps.Store.SaveCameraSource(r.Context(), store.CameraSourceSaveRequest{
		ID:                  raw.ID,
		Name:                raw.Name,
		SourceType:          raw.SourceType,
		RTSPURL:             raw.RTSPURL,
		MJPEGURL:            raw.MJPEGURL,
		RTMPURL:             raw.RTMPURL,
		GBPullURL:           raw.GBPullURL,
		GBDeviceID:          raw.GBDeviceID,
		GBChannelID:         raw.GBChannelID,
		GBServer:            raw.GBServer,
		GBTransport:         raw.GBTransport,
		ONVIFEndpoint:       raw.ONVIFEndpoint,
		ONVIFUsername:       raw.ONVIFUsername,
		ONVIFPassword:       raw.ONVIFPassword,
		ONVIFProfileToken:   raw.ONVIFProfileToken,
		USBDeviceName:       raw.USBDeviceName,
		USBDeviceResolution: raw.USBDeviceResolution,
		USBDeviceFramerate:  raw.USBDeviceFramerate,
		Description:         raw.Description,
		Enabled:             enabled,
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, saved)
}

func (m *cameraModule) delete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	affected, err := m.deps.Store.DeleteCameraSources(r.Context(), req.IDs)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"affected": affected,
	})
}

func (m *cameraModule) applyPush(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid camera source id", http.StatusBadRequest)
		return
	}
	camera, err := m.deps.Store.GetCameraSourceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	setting, err := m.deps.Store.GetPushSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}

	updateReq := buildPushUpdateRequest(setting)
	switch camera.SourceType {
	case store.CameraSourceTypeRTSP:
		updateReq.InputType = string(store.InputTypeRTSP)
		updateReq.RTSPURL = camera.RTSPURL
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = ""
	case store.CameraSourceTypeMJPEG:
		updateReq.InputType = string(store.InputTypeMJPEG)
		updateReq.MJPEGURL = camera.MJPEGURL
		updateReq.RTSPURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = ""
	case store.CameraSourceTypeONVIF:
		updateReq.InputType = string(store.InputTypeONVIF)
		updateReq.RTSPURL = camera.RTSPURL
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = ""
		updateReq.ONVIFEndpoint = camera.ONVIFEndpoint
		updateReq.ONVIFUsername = camera.ONVIFUsername
		updateReq.ONVIFPassword = camera.ONVIFPassword
		updateReq.ONVIFProfileToken = camera.ONVIFProfileToken
	case store.CameraSourceTypeUSB:
		updateReq.InputType = string(store.InputTypeUSBCamera)
		updateReq.RTSPURL = ""
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = ""
		updateReq.InputDeviceName = camera.USBDeviceName
		updateReq.InputDeviceResolution = camera.USBDeviceResolution
		updateReq.InputDeviceFramerate = camera.USBDeviceFramerate
	case store.CameraSourceTypeRTMP:
		updateReq.InputType = string(store.InputTypeRTMP)
		updateReq.RTSPURL = ""
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = camera.RTMPURL
		updateReq.GBPullURL = ""
	case store.CameraSourceTypeGB28181:
		updateReq.InputType = string(store.InputTypeGB28181)
		updateReq.RTSPURL = ""
		updateReq.MJPEGURL = ""
		updateReq.RTMPURL = ""
		updateReq.GBPullURL = camera.GBPullURL
	default:
		httpapi.Error(w, -1, "unsupported camera source type", http.StatusOK)
		return
	}

	updated, err := m.deps.Store.UpdatePushSetting(r.Context(), updateReq)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"camera":          camera,
		"pushSetting":     updated,
		"restartRequired": m.deps.Stream.Status() != store.PushStatusStopped,
	})
}

func (m *cameraModule) preview(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid camera source id", http.StatusBadRequest)
		return
	}
	camera, err := m.deps.Store.GetCameraSourceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	setting, err := buildCameraPreviewSetting(camera)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	command, args, err := streamsvc.BuildPreviewCommand(streamsvc.BuildContext{
		Setting:    setting,
		MediaDir:   m.deps.Config.MediaDir,
		FFmpegPath: m.deps.FFmpeg.BinaryPath(),
	}, previewOptionsFromRequest(r))
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if err := streamPreviewCommand(w, r, command, args, previewDebugEnabled(m.deps)); err != nil && r.Context().Err() == nil {
		log.Printf("[preview][camera:%d] stream failed: %v", id, err)
	}
}

func (m *cameraModule) previewWebRTCOffer(w http.ResponseWriter, r *http.Request) {
	if m.deps.WebRTCPreview == nil {
		httpapi.Error(w, -1, "webrtc preview service not configured", http.StatusInternalServerError)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid camera source id", http.StatusBadRequest)
		return
	}
	camera, err := m.deps.Store.GetCameraSourceByID(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	sourceURL, err := resolveCameraWebRTCPreviewSource(camera)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
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
	answer, err := m.deps.WebRTCPreview.StartRTSPPreview(r.Context(), sourceURL, req.SDP)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, answer)
}

func resolveCameraWebRTCPreviewSource(camera *store.CameraSource) (string, error) {
	if camera == nil {
		return "", errors.New("camera source is required")
	}
	switch camera.SourceType {
	case store.CameraSourceTypeRTSP, store.CameraSourceTypeONVIF:
		source := strings.TrimSpace(camera.RTSPURL)
		if !isRTSPSourceURL(source) {
			return "", errors.New("webrtc preview currently requires rtsp/rtsps url")
		}
		return source, nil
	case store.CameraSourceTypeGB28181:
		source := strings.TrimSpace(camera.GBPullURL)
		if !isRTSPSourceURL(source) {
			return "", errors.New("gb28181 webrtc preview currently requires rtsp/rtsps pull url")
		}
		return source, nil
	default:
		return "", errors.New("webrtc preview currently supports rtsp/onvif/gb28181(rtsp) camera source only")
	}
}

func buildCameraPreviewSetting(camera *store.CameraSource) (*store.PushSetting, error) {
	if camera == nil {
		return nil, errors.New("camera source is required")
	}
	setting := &store.PushSetting{
		InputType:             store.InputTypeRTSP,
		OutputResolution:      "1280x720",
		InputDeviceResolution: camera.USBDeviceResolution,
		InputDeviceFramerate:  camera.USBDeviceFramerate,
		RTSPURL:               strings.TrimSpace(camera.RTSPURL),
		MJPEGURL:              strings.TrimSpace(camera.MJPEGURL),
		RTMPURL:               strings.TrimSpace(camera.RTMPURL),
		GBPullURL:             strings.TrimSpace(camera.GBPullURL),
	}
	switch camera.SourceType {
	case store.CameraSourceTypeRTSP:
		setting.InputType = store.InputTypeRTSP
		if setting.RTSPURL == "" {
			return nil, errors.New("rtsp url is required for preview")
		}
	case store.CameraSourceTypeMJPEG:
		setting.InputType = store.InputTypeMJPEG
		if setting.MJPEGURL == "" {
			return nil, errors.New("mjpeg url is required for preview")
		}
	case store.CameraSourceTypeONVIF:
		setting.InputType = store.InputTypeONVIF
		if setting.RTSPURL == "" {
			return nil, errors.New("onvif preview requires resolved rtsp url")
		}
	case store.CameraSourceTypeUSB:
		setting.InputType = store.InputTypeUSBCamera
		setting.InputDeviceName = strings.TrimSpace(camera.USBDeviceName)
		if setting.InputDeviceResolution == "" {
			setting.InputDeviceResolution = "1280x720"
		}
		if setting.InputDeviceFramerate <= 0 {
			setting.InputDeviceFramerate = 30
		}
		if setting.InputDeviceName == "" {
			return nil, errors.New("usb device name is required for preview")
		}
	case store.CameraSourceTypeRTMP:
		setting.InputType = store.InputTypeRTMP
		if setting.RTMPURL == "" {
			return nil, errors.New("rtmp url is required for preview")
		}
	case store.CameraSourceTypeGB28181:
		setting.InputType = store.InputTypeGB28181
		if setting.GBPullURL == "" {
			return nil, errors.New("gb28181 pull url is required for preview")
		}
	default:
		return nil, errors.New("unsupported camera type")
	}
	return setting, nil
}

func buildPushUpdateRequest(item *store.PushSetting) store.PushSettingUpdateRequest {
	req := store.PushSettingUpdateRequest{
		Model:                 item.Model,
		FFmpegCommand:         item.FFmpegCommand,
		IsAutoRetry:           item.IsAutoRetry,
		RetryInterval:         item.RetryInterval,
		InputType:             string(item.InputType),
		OutputResolution:      item.OutputResolution,
		OutputQuality:         item.OutputQuality,
		OutputBitrateKbps:     item.OutputBitrateKbps,
		CustomOutputParams:    item.CustomOutputParams,
		CustomVideoCodec:      item.CustomVideoCodec,
		IsMute:                item.IsMute,
		InputScreen:           item.InputScreen,
		InputDeviceName:       item.InputDeviceName,
		InputDeviceResolution: item.InputDeviceResolution,
		InputDeviceFramerate:  item.InputDeviceFramerate,
		InputDevicePlugins:    item.InputDevicePlugins,
		RTSPURL:               item.RTSPURL,
		MJPEGURL:              item.MJPEGURL,
		RTMPURL:               item.RTMPURL,
		GBPullURL:             item.GBPullURL,
		ONVIFEndpoint:         item.ONVIFEndpoint,
		ONVIFUsername:         item.ONVIFUsername,
		ONVIFPassword:         item.ONVIFPassword,
		ONVIFProfileToken:     item.ONVIFProfileToken,
		MultiInputEnabled:     item.MultiInputEnabled,
		MultiInputLayout:      item.MultiInputLayout,
		MultiInputURLs:        item.MultiInputURLs,
		MultiInputMeta:        item.MultiInputMeta,
	}
	if item.VideoMaterialID != nil {
		req.VideoID = *item.VideoMaterialID
	}
	if item.AudioMaterialID != nil {
		audioID := *item.AudioMaterialID
		switch item.InputType {
		case store.InputTypeDesktop:
			if item.InputAudioSource == store.InputAudioSourceDevice {
				req.DesktopAudioFrom = true
				req.DesktopAudioDevice = item.InputAudioDeviceName
			} else {
				req.DesktopAudioID = audioID
			}
		case store.InputTypeUSBCamera, store.InputTypeCameraPlus:
			if item.InputAudioSource == store.InputAudioSourceDevice {
				req.InputDeviceAudioFrom = true
				req.InputDeviceAudioDevice = item.InputAudioDeviceName
			} else {
				req.InputDeviceAudioID = audioID
			}
		default:
			req.AudioID = audioID
		}
	} else {
		switch item.InputType {
		case store.InputTypeDesktop:
			if item.InputAudioSource == store.InputAudioSourceDevice {
				req.DesktopAudioFrom = true
				req.DesktopAudioDevice = item.InputAudioDeviceName
			}
		case store.InputTypeUSBCamera, store.InputTypeCameraPlus:
			if item.InputAudioSource == store.InputAudioSourceDevice {
				req.InputDeviceAudioFrom = true
				req.InputDeviceAudioDevice = item.InputAudioDeviceName
			}
		}
	}
	return req
}
