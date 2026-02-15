package handlers

import (
	"net/http"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
)

type logModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &logModule{deps: deps}
	})
}

func (m *logModule) Prefix() string {
	return m.deps.Config.APIBase + "/logs"
}

func (m *logModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/ffmpeg", Summary: "Get ffmpeg logs", Handler: m.ffmpegLogs},
	}
}

func (m *logModule) ffmpegLogs(w http.ResponseWriter, r *http.Request) {
	httpapi.OK(w, m.deps.Stream.Logs())
}
