package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	_ "bilibililivetools/gover/backend/api/handlers"
	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/logging"
	"bilibililivetools/gover/backend/router"
	authsvc "bilibililivetools/gover/backend/service/auth"
	"bilibililivetools/gover/backend/service/bilibili"
	ffsvc "bilibililivetools/gover/backend/service/ffmpeg"
	gbsvc "bilibililivetools/gover/backend/service/gb28181"
	"bilibililivetools/gover/backend/service/integration"
	"bilibililivetools/gover/backend/service/maintenance"
	"bilibililivetools/gover/backend/service/monitor"
	"bilibililivetools/gover/backend/service/onvif"
	"bilibililivetools/gover/backend/service/stream"
	"bilibililivetools/gover/backend/service/telemetry"
	previewsvc "bilibililivetools/gover/backend/service/webrtcpreview"
	"bilibililivetools/gover/backend/store"
)

type App struct {
	cfg           config.Config
	cfgManager    *config.Manager
	store         *store.Store
	stream        *stream.Manager
	server        *http.Server
	telemetry     *telemetry.Service
	integration   *integration.Service
	maintenance   *maintenance.Service
	gb28181       *gbsvc.Service
	webrtcPreview *previewsvc.Service
	frontendFS    fs.FS
	apiHandler    http.Handler
	routes        []router.Route
	openapiJSON   []byte
	logger        *logging.Manager
}

func New(cfgManager *config.Manager, embeddedFrontend fs.FS) (*App, error) {
	if cfgManager == nil {
		return nil, fmt.Errorf("config manager is required")
	}
	cfg := cfgManager.Current()
	log.Printf("[config] using config file: %s", cfg.ConfigFile)
	log.Printf("[config] ffmpeg path: %s", cfg.FFmpegPath)
	log.Printf("[config] ffprobe path: %s", cfg.FFprobePath)
	if err := os.MkdirAll(cfg.MediaDir, 0o755); err != nil {
		return nil, err
	}
	storeDB, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	ffmpegSvc := ffsvc.New(cfg.FFmpegPath, cfg.FFprobePath)
	bilibiliSvc := bilibili.New(storeDB, cfg)
	authService := authsvc.New(storeDB, 24*time.Hour)
	maintenanceSvc := maintenance.New(storeDB)
	monitorSvc := monitor.New(storeDB, cfg.LogBufferSize)
	onvifSvc := onvif.New()
	gbSvc := gbsvc.New(storeDB, cfg)
	streamMgr := stream.NewManager(storeDB, ffmpegSvc, bilibiliSvc, cfg.MediaDir, cfg.LogBufferSize, cfg.EnableDebugLogs || cfg.DebugMode)
	telemetrySvc := telemetry.New(storeDB, bilibiliSvc, streamMgr.Status)
	integrationSvc := integration.New(storeDB, streamMgr, bilibiliSvc, onvifSvc)
	webrtcPreviewSvc := previewsvc.New(24, cfg.EnableDebugLogs || cfg.DebugMode)
	loggerMgr, err := logging.New(cfg)
	if err != nil {
		storeDB.Close()
		return nil, err
	}

	deps := &router.Dependencies{
		Config:        cfg,
		ConfigMgr:     cfgManager,
		Store:         storeDB,
		Auth:          authService,
		FFmpeg:        ffmpegSvc,
		Stream:        streamMgr,
		GB28181:       gbSvc,
		Bilibili:      bilibiliSvc,
		Integration:   integrationSvc,
		Maintenance:   maintenanceSvc,
		Monitor:       monitorSvc,
		ONVIF:         onvifSvc,
		WebRTCPreview: webrtcPreviewSvc,
		FrontendFS:    embeddedFrontend,
	}
	apiHandler, routes := router.Build(deps)
	openapi, err := buildOpenAPISpec(routes)
	if err != nil {
		_ = loggerMgr.Close()
		storeDB.Close()
		return nil, err
	}

	frontendSub, err := fs.Sub(embeddedFrontend, "frontend")
	if err != nil {
		_ = loggerMgr.Close()
		storeDB.Close()
		return nil, fmt.Errorf("resolve embedded frontend failed: %w", err)
	}

	app := &App{
		cfg:           cfg,
		cfgManager:    cfgManager,
		store:         storeDB,
		stream:        streamMgr,
		telemetry:     telemetrySvc,
		integration:   integrationSvc,
		maintenance:   maintenanceSvc,
		gb28181:       gbSvc,
		webrtcPreview: webrtcPreviewSvc,
		frontendFS:    frontendSub,
		apiHandler:    apiHandler,
		routes:        routes,
		openapiJSON:   openapi,
		logger:        loggerMgr,
	}
	cfgManager.AddListener(func(newCfg config.Config) {
		log.Printf("[config] hot reload applied from %s", newCfg.ConfigFile)
		log.Printf("[config] ffmpeg path updated: %s", newCfg.FFmpegPath)
		log.Printf("[config] ffprobe path updated: %s", newCfg.FFprobePath)
		ffmpegSvc.UpdatePaths(newCfg.FFmpegPath, newCfg.FFprobePath)
		bilibiliSvc.UpdateConfig(newCfg)
		gbSvc.UpdateConfig(newCfg)
		streamMgr.UpdateDebug(newCfg.EnableDebugLogs || newCfg.DebugMode)
		webrtcPreviewSvc.UpdateDebug(newCfg.EnableDebugLogs || newCfg.DebugMode)
		if err := loggerMgr.Update(newCfg); err != nil {
			log.Printf("[config][warn] update logger failed: %v", err)
		}
	})
	app.server = &http.Server{
		Addr:              cfg.ListenAddr,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
		Handler:           app.mainMux(),
	}
	return app, nil
}

func (a *App) mainMux() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clean := path.Clean(r.URL.Path)
		if clean == "." {
			clean = "/"
		}

		if strings.HasPrefix(clean, a.cfg.APIBase+"/") || clean == a.cfg.APIBase {
			a.apiHandler.ServeHTTP(w, r)
			return
		}

		if clean == "/openapi.json" {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(a.openapiJSON)
			return
		}

		if strings.HasPrefix(clean, "/swagger") {
			a.serveSwagger(w, r, clean)
			return
		}

		a.serveFrontend(w, r, clean)
	})
}

func (a *App) serveSwagger(w http.ResponseWriter, r *http.Request, cleanPath string) {
	if cleanPath == "/swagger" || cleanPath == "/swagger/" {
		content, err := fs.ReadFile(a.frontendFS, "swagger/index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(content)
		return
	}
	target := ""
	suffix := strings.TrimPrefix(cleanPath, "/swagger/")
	target = path.Join("swagger", suffix)
	if _, err := fs.Stat(a.frontendFS, target); err == nil {
		http.FileServer(http.FS(a.frontendFS)).ServeHTTP(w, r)
		return
	}
	http.NotFound(w, r)
}

func (a *App) serveFrontend(w http.ResponseWriter, r *http.Request, cleanPath string) {
	if cleanPath == "/" {
		cleanPath = "/app/pages/home.html"
	}
	filePath := strings.TrimPrefix(cleanPath, "/")
	if filePath == "app" {
		filePath = "app/pages/home.html"
	}

	if info, err := fs.Stat(a.frontendFS, filePath); err == nil {
		if info.IsDir() {
			indexPath := path.Join(filePath, "index.html")
			if _, indexErr := fs.Stat(a.frontendFS, indexPath); indexErr == nil {
				filePath = indexPath
			}
		}
		if filePath != "" {
			if strings.HasSuffix(strings.ToLower(filePath), "index.html") {
				content, readErr := fs.ReadFile(a.frontendFS, filePath)
				if readErr == nil {
					w.Header().Set("Content-Type", "text/html; charset=utf-8")
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(content)
					return
				}
			}
			// Rewrite URL path to mapped embedded file to avoid root directory listing.
			rewritten := r.Clone(r.Context())
			rewritten.URL.Path = "/" + strings.TrimPrefix(filePath, "/")
			if ext := strings.TrimSpace(path.Ext(filePath)); ext != "" {
				if contentType := mime.TypeByExtension(ext); strings.TrimSpace(contentType) != "" {
					w.Header().Set("Content-Type", contentType)
				}
			}
			http.FileServer(http.FS(a.frontendFS)).ServeHTTP(w, rewritten)
			return
		}
		http.NotFound(w, r)
		return
	}

	// SPA fallback: unknown non-API routes go to frontend app entry.
	if _, err := fs.Stat(a.frontendFS, "app/pages/home.html"); err == nil {
		content, readErr := fs.ReadFile(a.frontendFS, "app/pages/home.html")
		if readErr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(content)
			return
		}
	}
	http.NotFound(w, r)
}

func (a *App) Run() error {
	a.cfgManager.StartWatching()
	startupCfg := a.cfgManager.Current()
	if startupCfg.AutoStartPush {
		go func() {
			if err := a.stream.Start(context.Background(), true); err != nil {
				log.Printf("startup stream skipped: %v", err)
			}
		}()
	} else {
		log.Printf("startup auto push disabled by config (autoStartPush=false)")
	}
	a.telemetry.Start()
	a.integration.Start()
	a.maintenance.Start()
	if a.gb28181 != nil && a.cfg.GB28181Enabled {
		if err := a.gb28181.Start(context.Background()); err != nil {
			log.Printf("startup gb28181 skipped: %v", err)
		}
	}
	log.Printf("gover listening on %s", a.cfg.ListenAddr)
	return a.server.ListenAndServe()
}

func (a *App) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.cfgManager.StopWatching()
	a.maintenance.Stop()
	a.integration.Stop()
	a.telemetry.Stop()
	if a.gb28181 != nil {
		_ = a.gb28181.Stop(ctx)
	}
	if a.webrtcPreview != nil {
		a.webrtcPreview.CloseAll()
	}
	_ = a.stream.Stop(ctx)
	shutdownErr := a.server.Shutdown(ctx)
	closeErr := a.store.Close()
	if a.logger != nil {
		_ = a.logger.Close()
	}
	if shutdownErr != nil {
		return shutdownErr
	}
	return closeErr
}

func buildOpenAPISpec(routes []router.Route) ([]byte, error) {
	paths := map[string]map[string]any{}
	for _, rt := range routes {
		method := strings.ToLower(rt.Method)
		if method != "get" && method != "post" {
			continue
		}
		if _, ok := paths[rt.Pattern]; !ok {
			paths[rt.Pattern] = map[string]any{}
		}
		operation := map[string]any{
			"summary":     rt.Summary,
			"description": rt.Description,
			"operationId": buildOperationID(method, rt.Pattern),
			"tags":        []string{deriveRouteTag(rt.Pattern)},
			"responses": map[string]any{
				"200": map[string]any{
					"description": "Success",
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": map[string]any{
								"$ref": "#/components/schemas/ResultEnvelope",
							},
						},
					},
				},
				"default": map[string]any{
					"description": "Error payload",
					"content": map[string]any{
						"application/json": map[string]any{
							"schema": map[string]any{
								"$ref": "#/components/schemas/ResultEnvelope",
							},
						},
					},
				},
			},
		}
		if method == "post" {
			operation["requestBody"] = map[string]any{
				"required": false,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type": "object",
						},
					},
				},
			}
		}
		if example := routeExample(method, rt.Pattern); example != nil {
			if requestExample, ok := example["request"]; ok && method == "post" {
				requestBody := operation["requestBody"].(map[string]any)
				content := requestBody["content"].(map[string]any)
				jsonContent := content["application/json"].(map[string]any)
				jsonContent["example"] = requestExample
			}
			if responseExample, ok := example["response"]; ok {
				responses := operation["responses"].(map[string]any)
				okResponse := responses["200"].(map[string]any)
				content := okResponse["content"].(map[string]any)
				jsonContent := content["application/json"].(map[string]any)
				jsonContent["example"] = responseExample
			}
		}
		paths[rt.Pattern][method] = operation
	}
	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":       "BilibiliLiveTools Go Rewrite API",
			"version":     "0.2.0",
			"description": "REST API for streaming, Bilibili room operations, ONVIF PTZ, danmaku and integration workflows.",
		},
		"servers": []map[string]any{{"url": "/"}},
		"paths":   paths,
		"components": map[string]any{
			"schemas": map[string]any{
				"ResultEnvelope": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"code": map[string]any{
							"type":    "integer",
							"example": 0,
						},
						"message": map[string]any{
							"type":    "string",
							"example": "Success",
						},
						"data": map[string]any{
							"nullable": true,
						},
					},
				},
			},
		},
	}
	return json.MarshalIndent(spec, "", "  ")
}

func buildOperationID(method string, pattern string) string {
	segments := strings.Split(strings.Trim(pattern, "/"), "/")
	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, strings.ToLower(method))
	for _, segment := range segments {
		if segment == "" {
			continue
		}
		segment = strings.Trim(segment, "{}")
		segment = strings.ReplaceAll(segment, "-", "_")
		parts = append(parts, segment)
	}
	return strings.Join(parts, "_")
}

func deriveRouteTag(pattern string) string {
	segments := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(segments) == 0 {
		return "general"
	}
	for idx, segment := range segments {
		if strings.HasPrefix(segment, "v") && idx+1 < len(segments) {
			return strings.ReplaceAll(segments[idx+1], "-", "_")
		}
	}
	if len(segments) >= 2 {
		return strings.ReplaceAll(segments[1], "-", "_")
	}
	return strings.ReplaceAll(segments[0], "-", "_")
}

func routeExample(method string, pattern string) map[string]any {
	key := strings.ToUpper(method) + " " + pattern
	switch key {
	case "POST /api/v1/push/setting":
		return map[string]any{
			"request": map[string]any{
				"model":            1,
				"inputType":        "rtsp",
				"outputResolution": "1920x1080",
				"outputQuality":    2,
				"rtspUrl":          "rtsp://127.0.0.1/live/stream1",
				"rtmpUrl":          "",
				"gbPullUrl":        "",
				"isAutoRetry":      true,
				"retryInterval":    30,
			},
		}
	case "POST /api/v1/integration/danmaku/dispatch":
		return map[string]any{
			"request": map[string]any{
				"roomId":  123456,
				"uid":     10001,
				"uname":   "tester",
				"content": "向左",
				"source":  "bilibili.danmaku",
			},
		}
	case "POST /api/v1/ptz/command":
		return map[string]any{
			"request": map[string]any{
				"endpoint":    "http://192.168.1.20/onvif/device_service",
				"username":    "admin",
				"password":    "123456",
				"action":      "left",
				"speed":       0.3,
				"durationMs":  700,
				"presetToken": "",
				"pan":         0,
				"tilt":        0,
				"zoom":        0,
			},
		}
	case "POST /api/v1/integration/notify":
		return map[string]any{
			"request": map[string]any{
				"eventType": "live.started",
				"payload": map[string]any{
					"roomId": 123456,
				},
			},
		}
	case "POST /api/v1/cameras/save":
		return map[string]any{
			"request": map[string]any{
				"name":        "GB28181-EastGate",
				"sourceType":  "gb28181",
				"gbPullUrl":   "rtsp://127.0.0.1/live/34020000001320000001",
				"gbDeviceId":  "34020000001320000001",
				"gbChannelId": "34020000001310000001",
				"gbServer":    "192.168.1.100:5060",
				"gbTransport": "udp",
				"enabled":     true,
			},
		}
	case "POST /api/v1/cameras/delete":
		return map[string]any{
			"request": map[string]any{
				"ids": []int64{1, 2},
			},
		}
	case "POST /api/v1/cameras/{id}/apply-push":
		return map[string]any{
			"request": map[string]any{},
		}
	case "POST /api/v1/integration/bilibili/alert-setting":
		return map[string]any{
			"request": map[string]any{
				"enabled":         true,
				"windowMinutes":   10,
				"threshold":       8,
				"cooldownMinutes": 15,
				"webhookEvent":    "bilibili.api.alert",
			},
		}
	case "POST /api/v1/integration/bilibili/errors/check-alert":
		return map[string]any{
			"request": map[string]any{},
		}
	case "POST /api/v1/maintenance/setting":
		return map[string]any{
			"request": map[string]any{
				"enabled":       true,
				"retentionDays": 7,
				"autoVacuum":    true,
			},
		}
	case "POST /api/v1/maintenance/cleanup":
		return map[string]any{
			"request": map[string]any{
				"days":   7,
				"vacuum": true,
			},
		}
	case "POST /api/v1/maintenance/vacuum":
		return map[string]any{
			"request": map[string]any{},
		}
	case "POST /api/v1/maintenance/cancel":
		return map[string]any{
			"request": map[string]any{
				"jobId": "",
			},
		}
	default:
		return nil
	}
}

func (a *App) RouteList() []router.Route {
	items := make([]router.Route, len(a.routes))
	copy(items, a.routes)
	return items
}

func ensurePath(target string) error {
	dir := filepath.Dir(target)
	if dir == "." || dir == string(filepath.Separator) {
		return nil
	}
	return os.MkdirAll(dir, 0o755)
}
