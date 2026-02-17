package router

import (
	"io/fs"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/httpapi"
	authsvc "bilibililivetools/gover/backend/service/auth"
	"bilibililivetools/gover/backend/service/bilibili"
	ffsvc "bilibililivetools/gover/backend/service/ffmpeg"
	gbsvc "bilibililivetools/gover/backend/service/gb28181"
	"bilibililivetools/gover/backend/service/integration"
	"bilibililivetools/gover/backend/service/maintenance"
	"bilibililivetools/gover/backend/service/monitor"
	"bilibililivetools/gover/backend/service/onvif"
	"bilibililivetools/gover/backend/service/stream"
	previewsvc "bilibililivetools/gover/backend/service/webrtcpreview"
	"bilibililivetools/gover/backend/store"
)

type Dependencies struct {
	Config        config.Config
	ConfigMgr     *config.Manager
	Store         *store.Store
	Auth          *authsvc.Service
	FFmpeg        *ffsvc.Service
	Stream        *stream.Manager
	GB28181       *gbsvc.Service
	Bilibili      bilibili.Service
	Integration   *integration.Service
	Maintenance   *maintenance.Service
	Monitor       *monitor.Service
	ONVIF         *onvif.Service
	WebRTCPreview *previewsvc.Service
	FrontendFS    fs.FS
}

type Route struct {
	Method      string
	Pattern     string
	Summary     string
	Description string
	Handler     http.HandlerFunc
}

type Module interface {
	Prefix() string
	Routes() []Route
}

type Factory func(*Dependencies) Module

var (
	registryMu sync.Mutex
	registry   []Factory
)

func Register(factory Factory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, factory)
}

func Build(deps *Dependencies) (http.Handler, []Route) {
	r := chi.NewRouter()
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(httpapi.CORS(deps.Config.AllowOrigin))
	r.Use(httpapi.Logging)
	if deps.Auth != nil {
		r.Use(httpapi.AuthRequired(deps.Auth, deps.Config.APIBase))
	}

	routes := make([]Route, 0, 128)
	modules := instantiateModules(deps)
	for _, mod := range modules {
		prefix := normalizePrefix(mod.Prefix())
		for _, rt := range mod.Routes() {
			path := normalizePath(prefix, rt.Pattern)
			bind(r, strings.ToUpper(strings.TrimSpace(rt.Method)), path, rt.Handler)
			routes = append(routes, Route{
				Method:      strings.ToUpper(strings.TrimSpace(rt.Method)),
				Pattern:     path,
				Summary:     rt.Summary,
				Description: rt.Description,
			})
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Pattern == routes[j].Pattern {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Pattern < routes[j].Pattern
	})
	return r, routes
}

func instantiateModules(deps *Dependencies) []Module {
	registryMu.Lock()
	defer registryMu.Unlock()
	modules := make([]Module, 0, len(registry))
	for _, factory := range registry {
		modules = append(modules, factory(deps))
	}
	return modules
}

func bind(r chi.Router, method string, path string, handler http.HandlerFunc) {
	switch method {
	case http.MethodGet:
		r.Get(path, handler)
	case http.MethodPost:
		r.Post(path, handler)
	default:
		r.MethodFunc(method, path, handler)
	}
}

func normalizePrefix(prefix string) string {
	if prefix == "" {
		return ""
	}
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}
	return strings.TrimSuffix(prefix, "/")
}

func normalizePath(prefix string, pattern string) string {
	if pattern == "" || pattern == "/" {
		if prefix == "" {
			return "/"
		}
		return prefix
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/" + pattern
	}
	if prefix == "" {
		return pattern
	}
	if pattern == "/" {
		return prefix
	}
	return prefix + pattern
}
