package handlers

import (
	"net/http"
	"runtime"
	"time"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
)

type healthModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &healthModule{deps: deps}
	})
}

func (m *healthModule) Prefix() string {
	return m.deps.Config.APIBase
}

func (m *healthModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/health", Summary: "Health check", Handler: m.health},
		{Method: http.MethodGet, Pattern: "/capabilities", Summary: "Capability manifest", Handler: m.capabilities},
	}
}

func (m *healthModule) health(w http.ResponseWriter, r *http.Request) {
	type payload struct {
		Status    string `json:"status"`
		Now       string `json:"now"`
		GoVersion string `json:"goVersion"`
	}
	httpapi.OK(w, payload{
		Status:    "ok",
		Now:       time.Now().Format(time.RFC3339),
		GoVersion: runtime.Version(),
	})
}

func (m *healthModule) capabilities(w http.ResponseWriter, r *http.Request) {
	type capability struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	httpapi.OK(w, []capability{
		{Name: "push.video", Description: "Stream from local video files"},
		{Name: "push.usb_camera", Description: "Stream from USB camera"},
		{Name: "push.rtsp", Description: "Stream from RTSP source"},
		{Name: "push.mjpeg", Description: "Stream from MJPEG source"},
		{Name: "integration.danmaku_ptz", Description: "Command mapping from danmaku to PTZ actions"},
		{Name: "integration.webhook", Description: "Outbound webhook integration with async queue"},
		{Name: "integration.bot", Description: "Bot command integration with TG/DingTalk/Pushoo notify"},
		{Name: "integration.api_key", Description: "Plain text API key configuration"},
		{Name: "docs.swagger", Description: "Offline Swagger UI and OpenAPI endpoint"},
	})
}
