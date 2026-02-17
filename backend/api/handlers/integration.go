package handlers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	intsvc "bilibililivetools/gover/backend/service/integration"
	"bilibililivetools/gover/backend/service/onvif"
	"bilibililivetools/gover/backend/store"
)

var webhookHTTPClient = &http.Client{Timeout: 12 * time.Second}

var providerInboundReplayCache = struct {
	mu    sync.Mutex
	items map[string]time.Time
}{
	items: make(map[string]time.Time),
}

type integrationModule struct {
	deps *router.Dependencies
}

type dispatchDanmakuRequest struct {
	RoomID     int64  `json:"roomId"`
	UID        int64  `json:"uid"`
	Uname      string `json:"uname"`
	Content    string `json:"content"`
	RawPayload string `json:"rawPayload"`
	Source     string `json:"source"`
}

type webhookSendResult struct {
	RequestBody  string
	ResponseBody string
	ResponseCode int
	DurationMS   int64
}

type bilibiliErrorEvent struct {
	ID        int64     `json:"id"`
	Endpoint  string    `json:"endpoint"`
	Method    string    `json:"method"`
	Detail    string    `json:"detail"`
	Attempt   int       `json:"attempt"`
	Retryable bool      `json:"retryable"`
	Category  string    `json:"category"`
	Advice    string    `json:"advice"`
	CreatedAt time.Time `json:"createdAt"`
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &integrationModule{deps: deps}
	})
}

func (m *integrationModule) Prefix() string {
	return m.deps.Config.APIBase
}

func (m *integrationModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/integration/danmaku-rules", Summary: "List danmaku PTZ rules", Handler: m.listDanmakuRules},
		{Method: http.MethodPost, Pattern: "/integration/danmaku-rules", Summary: "Save danmaku PTZ rule", Handler: m.saveDanmakuRule},
		{Method: http.MethodPost, Pattern: "/integration/danmaku/dispatch", Summary: "Ingest danmaku and dispatch rules", Handler: m.dispatchDanmaku},
		{Method: http.MethodGet, Pattern: "/integration/danmaku/consumer/setting", Summary: "Get danmaku consumer setting", Handler: m.getDanmakuConsumerSetting},
		{Method: http.MethodPost, Pattern: "/integration/danmaku/consumer/setting", Summary: "Save danmaku consumer setting", Handler: m.saveDanmakuConsumerSetting},
		{Method: http.MethodGet, Pattern: "/integration/danmaku/consumer/status", Summary: "Get danmaku consumer runtime status", Handler: m.danmakuConsumerStatus},
		{Method: http.MethodPost, Pattern: "/integration/danmaku/consumer/poll-once", Summary: "Poll danmaku consumer once immediately", Handler: m.danmakuConsumerPollOnce},
		{Method: http.MethodGet, Pattern: "/integration/webhooks", Summary: "List webhook settings", Handler: m.listWebhooks},
		{Method: http.MethodPost, Pattern: "/integration/webhooks", Summary: "Save webhook setting", Handler: m.saveWebhook},
		{Method: http.MethodGet, Pattern: "/integration/webhooks/delivery-logs", Summary: "List webhook delivery logs", Handler: m.listWebhookDeliveryLogs},
		{Method: http.MethodGet, Pattern: "/integration/tasks", Summary: "List integration async tasks", Handler: m.listIntegrationTasks},
		{Method: http.MethodGet, Pattern: "/integration/tasks/summary", Summary: "Get integration async task summary", Handler: m.integrationTaskSummary},
		{Method: http.MethodPost, Pattern: "/integration/tasks/retry", Summary: "Retry dead/cancelled integration task", Handler: m.retryIntegrationTask},
		{Method: http.MethodPost, Pattern: "/integration/tasks/retry-batch", Summary: "Retry dead/cancelled tasks in batch", Handler: m.retryIntegrationTaskBatch},
		{Method: http.MethodPost, Pattern: "/integration/tasks/cancel", Summary: "Cancel pending/running integration task", Handler: m.cancelIntegrationTask},
		{Method: http.MethodPost, Pattern: "/integration/tasks/priority", Summary: "Update integration task priority", Handler: m.updateIntegrationTaskPriority},
		{Method: http.MethodGet, Pattern: "/integration/tasks/queue-setting", Summary: "Get integration task queue setting", Handler: m.getIntegrationQueueSetting},
		{Method: http.MethodPost, Pattern: "/integration/tasks/queue-setting", Summary: "Save integration task queue setting", Handler: m.saveIntegrationQueueSetting},
		{Method: http.MethodGet, Pattern: "/integration/features", Summary: "Get integration feature toggles", Handler: m.getIntegrationFeatures},
		{Method: http.MethodPost, Pattern: "/integration/features", Summary: "Save integration feature toggles", Handler: m.saveIntegrationFeatures},
		{Method: http.MethodGet, Pattern: "/integration/runtime/memory", Summary: "Get process memory runtime stats", Handler: m.runtimeMemoryStats},
		{Method: http.MethodPost, Pattern: "/integration/runtime/gc", Summary: "Trigger runtime GC and return memory stats", Handler: m.runtimeForceGC},
		{Method: http.MethodGet, Pattern: "/integration/api-keys", Summary: "List plain text API keys", Handler: m.listAPIKeys},
		{Method: http.MethodPost, Pattern: "/integration/api-keys", Summary: "Save plain text API key", Handler: m.saveAPIKey},
		{Method: http.MethodPost, Pattern: "/integration/live-events", Summary: "Record live event payload", Handler: m.saveLiveEvent},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/errors", Summary: "List bilibili api error events", Handler: m.listBilibiliAPIErrors},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/error-logs", Summary: "List full bilibili api error logs", Handler: m.listBilibiliAPIErrorLogs},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/error-logs/{id}", Summary: "Get full bilibili api error log detail", Handler: m.getBilibiliAPIErrorLog},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/errors/summary", Summary: "Summarize bilibili api errors by endpoint", Handler: m.bilibiliErrorSummary},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/errors/insights", Summary: "Analyze bilibili api errors with categories", Handler: m.bilibiliErrorInsights},
		{Method: http.MethodGet, Pattern: "/integration/bilibili/alert-setting", Summary: "Get bilibili api alert setting", Handler: m.getBilibiliAlertSetting},
		{Method: http.MethodPost, Pattern: "/integration/bilibili/alert-setting", Summary: "Save bilibili api alert setting", Handler: m.saveBilibiliAlertSetting},
		{Method: http.MethodPost, Pattern: "/integration/bilibili/errors/check-alert", Summary: "Check and send bilibili api error alert", Handler: m.checkBilibiliAPIAlert},
		{Method: http.MethodGet, Pattern: "/ptz/discover", Summary: "Discover ONVIF devices via WS-Discovery", Handler: m.ptzDiscover},
		{Method: http.MethodGet, Pattern: "/ptz/capabilities", Summary: "Read ONVIF capabilities", Handler: m.ptzCapabilities},
		{Method: http.MethodPost, Pattern: "/ptz/profiles", Summary: "Read ONVIF profiles", Handler: m.ptzProfiles},
		{Method: http.MethodPost, Pattern: "/ptz/command", Summary: "Execute PTZ command", Handler: m.ptzCommand},
		{Method: http.MethodPost, Pattern: "/integration/webhooks/test", Summary: "Send test webhook payload", Handler: m.testWebhook},
		{Method: http.MethodPost, Pattern: "/integration/notify", Summary: "Dispatch notify event to enabled webhooks", Handler: m.notifyWebhooks},
		{Method: http.MethodPost, Pattern: "/integration/bot/command", Summary: "Bot command endpoint for TG/DingTalk/Pushoo", Handler: m.botCommand},
		{Method: http.MethodPost, Pattern: "/integration/provider/inbound/{provider}", Summary: "Provider inbound webhook with signature and anti-replay", Handler: m.providerInboundWebhook},
	}
}

func (m *integrationModule) ensureFeaturesEnabled(w http.ResponseWriter, r *http.Request, features ...intsvc.FeatureName) bool {
	for _, feature := range features {
		if err := m.deps.Integration.EnsureFeatureEnabled(r.Context(), feature); err != nil {
			httpapi.Error(w, -1, err.Error(), http.StatusOK)
			return false
		}
	}
	return true
}

func (m *integrationModule) featureEffectiveState(ctx context.Context) map[string]bool {
	features := []intsvc.FeatureName{
		intsvc.FeatureDanmakuConsumer,
		intsvc.FeatureWebhook,
		intsvc.FeatureBot,
		intsvc.FeatureAdvancedStats,
		intsvc.FeatureTaskQueue,
	}
	state := make(map[string]bool, len(features))
	for _, feature := range features {
		enabled, err := m.deps.Integration.IsFeatureEnabled(ctx, feature)
		state[string(feature)] = err == nil && enabled
	}
	return state
}

func (m *integrationModule) listDanmakuRules(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntOrDefault(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	items, err := m.deps.Integration.ListDanmakuRules(r.Context(), limit, offset)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) saveDanmakuRule(w http.ResponseWriter, r *http.Request) {
	var req store.DanmakuPTZRule
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Keyword) == "" {
		httpapi.Error(w, -1, "keyword is required", http.StatusOK)
		return
	}
	if req.PTZSpeed <= 0 {
		req.PTZSpeed = 1
	}
	if req.PTZDirection == "" {
		req.PTZDirection = "center"
	}
	if req.Action == "" {
		req.Action = "ptz"
	}
	if err := m.deps.Integration.SaveDanmakuRule(r.Context(), req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *integrationModule) dispatchDanmaku(w http.ResponseWriter, r *http.Request) {
	var req dispatchDanmakuRequest
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := m.deps.Integration.DispatchDanmaku(r.Context(), intsvc.DanmakuDispatchRequest{
		RoomID:     req.RoomID,
		UID:        req.UID,
		Uname:      req.Uname,
		Content:    req.Content,
		RawPayload: req.RawPayload,
		Source:     req.Source,
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *integrationModule) getDanmakuConsumerSetting(w http.ResponseWriter, r *http.Request) {
	item, err := m.deps.Integration.GetDanmakuConsumerSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"setting": item,
		"runtime": m.deps.Integration.ConsumerRuntime(),
	})
}

func (m *integrationModule) saveDanmakuConsumerSetting(w http.ResponseWriter, r *http.Request) {
	var req store.DanmakuConsumerSetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	item, err := m.deps.Integration.SaveDanmakuConsumerSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"setting": item,
		"runtime": m.deps.Integration.ConsumerRuntime(),
	})
}

func (m *integrationModule) danmakuConsumerStatus(w http.ResponseWriter, r *http.Request) {
	item, err := m.deps.Integration.GetDanmakuConsumerSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"setting": item,
		"runtime": m.deps.Integration.ConsumerRuntime(),
	})
}

func (m *integrationModule) danmakuConsumerPollOnce(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureDanmakuConsumer) {
		return
	}
	result, err := m.deps.Integration.PollDanmakuConsumerOnce(r.Context(), true)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *integrationModule) listWebhooks(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntOrDefault(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	items, err := m.deps.Integration.ListWebhooks(r.Context(), limit, offset)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) saveWebhook(w http.ResponseWriter, r *http.Request) {
	var req store.WebhookSetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if err := m.deps.Integration.SaveWebhook(r.Context(), req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *integrationModule) listWebhookDeliveryLogs(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	webhookID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("webhookId")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			webhookID = parsed
		}
	}
	items, err := m.deps.Integration.ListWebhookDeliveryLogs(r.Context(), limit, webhookID)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) listIntegrationTasks(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	taskType := strings.TrimSpace(r.URL.Query().Get("type"))
	items, err := m.deps.Integration.ListIntegrationTasks(r.Context(), limit, status, taskType)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) integrationTaskSummary(w http.ResponseWriter, r *http.Request) {
	summary, err := m.deps.Integration.IntegrationTaskSummary(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"summary": summary,
		"runtime": map[string]any{
			"consumer": m.deps.Integration.ConsumerRuntime(),
		},
	})
}

func (m *integrationModule) retryIntegrationTask(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue) {
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID <= 0 {
		httpapi.Error(w, -1, "id is required", http.StatusOK)
		return
	}
	if err := m.deps.Integration.RetryIntegrationTask(r.Context(), req.ID); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"id":      req.ID,
		"message": "queued",
	})
}

func (m *integrationModule) retryIntegrationTaskBatch(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue) {
		return
	}
	var req struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
		Type   string  `json:"type"`
		Limit  int     `json:"limit"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := m.deps.Integration.RetryIntegrationTasksBatch(r.Context(), req.IDs, req.Status, req.Type, req.Limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"updated": updated,
		"status":  req.Status,
		"type":    req.Type,
	})
}

func (m *integrationModule) cancelIntegrationTask(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue) {
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID <= 0 {
		httpapi.Error(w, -1, "id is required", http.StatusOK)
		return
	}
	if err := m.deps.Integration.CancelIntegrationTask(r.Context(), req.ID); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"id":      req.ID,
		"message": "cancelled",
	})
}

func (m *integrationModule) updateIntegrationTaskPriority(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue) {
		return
	}
	var req struct {
		ID       int64 `json:"id"`
		Priority int   `json:"priority"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.ID <= 0 {
		httpapi.Error(w, -1, "id is required", http.StatusOK)
		return
	}
	if req.Priority <= 0 {
		httpapi.Error(w, -1, "priority must be greater than 0", http.StatusOK)
		return
	}
	if err := m.deps.Integration.UpdateIntegrationTaskPriority(r.Context(), req.ID, req.Priority); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"id":       req.ID,
		"priority": req.Priority,
		"message":  "updated",
	})
}

func (m *integrationModule) getIntegrationQueueSetting(w http.ResponseWriter, r *http.Request) {
	item, err := m.deps.Integration.GetQueueSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *integrationModule) saveIntegrationQueueSetting(w http.ResponseWriter, r *http.Request) {
	var req store.IntegrationQueueSetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	item, err := m.deps.Integration.SaveQueueSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *integrationModule) getIntegrationFeatures(w http.ResponseWriter, r *http.Request) {
	item, err := m.deps.Integration.GetFeatureSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"setting":   item,
		"effective": m.featureEffectiveState(r.Context()),
		"supported": []string{
			string(intsvc.FeatureDanmakuConsumer),
			string(intsvc.FeatureWebhook),
			string(intsvc.FeatureBot),
			string(intsvc.FeatureAdvancedStats),
			string(intsvc.FeatureTaskQueue),
		},
	})
}

func (m *integrationModule) saveIntegrationFeatures(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SimpleMode            *bool `json:"simpleMode"`
		EnableDanmakuConsumer *bool `json:"enableDanmakuConsumer"`
		EnableWebhook         *bool `json:"enableWebhook"`
		EnableBot             *bool `json:"enableBot"`
		EnableAdvancedStats   *bool `json:"enableAdvancedStats"`
		EnableTaskQueue       *bool `json:"enableTaskQueue"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	current, err := m.deps.Integration.GetFeatureSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	next := *current
	if req.SimpleMode != nil {
		next.SimpleMode = *req.SimpleMode
	}
	if req.EnableDanmakuConsumer != nil {
		next.EnableDanmakuConsumer = *req.EnableDanmakuConsumer
	}
	if req.EnableWebhook != nil {
		next.EnableWebhook = *req.EnableWebhook
	}
	if req.EnableBot != nil {
		next.EnableBot = *req.EnableBot
	}
	if req.EnableAdvancedStats != nil {
		next.EnableAdvancedStats = *req.EnableAdvancedStats
	}
	if req.EnableTaskQueue != nil {
		next.EnableTaskQueue = *req.EnableTaskQueue
	}
	updated, err := m.deps.Integration.SaveFeatureSetting(r.Context(), next)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"message":   "saved",
		"setting":   updated,
		"effective": m.featureEffectiveState(r.Context()),
	})
}

func (m *integrationModule) runtimeMemoryStats(w http.ResponseWriter, r *http.Request) {
	httpapi.OK(w, map[string]any{
		"memory":    m.deps.Integration.RuntimeMemoryStats(),
		"effective": m.featureEffectiveState(r.Context()),
	})
}

func (m *integrationModule) runtimeForceGC(w http.ResponseWriter, r *http.Request) {
	httpapi.OK(w, map[string]any{
		"memory":    m.deps.Integration.ForceMemoryGC(),
		"effective": m.featureEffectiveState(r.Context()),
	})
}

func (m *integrationModule) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	offset := parseIntOrDefault(r.URL.Query().Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	items, err := m.deps.Integration.ListAPIKeys(r.Context(), limit, offset)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) saveAPIKey(w http.ResponseWriter, r *http.Request) {
	var req store.APIKeySetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		httpapi.Error(w, -1, "name is required", http.StatusOK)
		return
	}
	if err := m.deps.Integration.SaveAPIKey(r.Context(), req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *integrationModule) saveLiveEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventType string          `json:"eventType"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.EventType) == "" {
		httpapi.Error(w, -1, "eventType is required", http.StatusOK)
		return
	}
	if err := m.deps.Integration.SaveLiveEvent(r.Context(), req.EventType, string(req.Payload)); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *integrationModule) listBilibiliAPIErrors(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	items, err := m.deps.Integration.ListLiveEventsByType(r.Context(), "bilibili.api.error", limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) listBilibiliAPIErrorLogs(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	endpointKeyword := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	items, err := m.deps.Integration.ListBilibiliAPIErrorLogs(r.Context(), limit, endpointKeyword)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *integrationModule) getBilibiliAPIErrorLog(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimSpace(chi.URLParam(r, "id")), 10, 64)
	if err != nil || id <= 0 {
		httpapi.Error(w, -1, "invalid id", http.StatusBadRequest)
		return
	}
	item, err := m.deps.Integration.GetBilibiliAPIErrorLog(r.Context(), id)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *integrationModule) bilibiliErrorSummary(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 500)
	items, err := m.deps.Integration.ListLiveEventsByType(r.Context(), "bilibili.api.error", limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	type bucket struct {
		Endpoint  string `json:"endpoint"`
		Method    string `json:"method"`
		Count     int    `json:"count"`
		Retryable int    `json:"retryable"`
	}
	agg := map[string]*bucket{}
	for _, item := range items {
		var payload struct {
			Endpoint  string `json:"endpoint"`
			Method    string `json:"method"`
			Retryable bool   `json:"retryable"`
		}
		if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil {
			continue
		}
		endpoint := strings.TrimSpace(payload.Endpoint)
		if endpoint == "" {
			endpoint = "unknown"
		}
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		key := method + " " + endpoint
		if _, ok := agg[key]; !ok {
			agg[key] = &bucket{
				Endpoint: endpoint,
				Method:   method,
			}
		}
		agg[key].Count++
		if payload.Retryable {
			agg[key].Retryable++
		}
	}
	result := make([]bucket, 0, len(agg))
	for _, item := range agg {
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Endpoint < result[j].Endpoint
		}
		return result[i].Count > result[j].Count
	})
	httpapi.OK(w, map[string]any{
		"totalEvents": len(items),
		"summary":     result,
	})
}

func (m *integrationModule) bilibiliErrorInsights(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 500)
	windowMinutes := parseIntOrDefault(r.URL.Query().Get("windowMinutes"), 60)
	if windowMinutes <= 0 {
		windowMinutes = 60
	}
	if windowMinutes > 24*60 {
		windowMinutes = 24 * 60
	}

	events, err := m.loadBilibiliErrorEvents(r.Context(), limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}

	cutoff := time.Now().Add(-time.Duration(windowMinutes) * time.Minute)
	windowed := make([]bilibiliErrorEvent, 0, len(events))
	for _, item := range events {
		if item.CreatedAt.After(cutoff) {
			windowed = append(windowed, item)
		}
	}

	categoryCount := map[string]int{}
	endpointCount := map[string]int{}
	retryableCount := 0
	for _, item := range windowed {
		categoryCount[item.Category]++
		endpointKey := strings.ToUpper(strings.TrimSpace(item.Method)) + " " + strings.TrimSpace(item.Endpoint)
		endpointCount[endpointKey]++
		if item.Retryable {
			retryableCount++
		}
	}

	type countItem struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	categoryList := make([]countItem, 0, len(categoryCount))
	for key, value := range categoryCount {
		categoryList = append(categoryList, countItem{Name: key, Count: value})
	}
	sort.Slice(categoryList, func(i, j int) bool {
		if categoryList[i].Count == categoryList[j].Count {
			return categoryList[i].Name < categoryList[j].Name
		}
		return categoryList[i].Count > categoryList[j].Count
	})

	endpointList := make([]countItem, 0, len(endpointCount))
	for key, value := range endpointCount {
		endpointList = append(endpointList, countItem{Name: key, Count: value})
	}
	sort.Slice(endpointList, func(i, j int) bool {
		if endpointList[i].Count == endpointList[j].Count {
			return endpointList[i].Name < endpointList[j].Name
		}
		return endpointList[i].Count > endpointList[j].Count
	})

	sampleLimit := 30
	if len(windowed) < sampleLimit {
		sampleLimit = len(windowed)
	}
	httpapi.OK(w, map[string]any{
		"windowMinutes": windowMinutes,
		"totalInWindow": len(windowed),
		"retryable":     retryableCount,
		"categoryTop":   categoryList,
		"endpointTop":   endpointList,
		"samples":       windowed[:sampleLimit],
	})
}

func (m *integrationModule) getBilibiliAlertSetting(w http.ResponseWriter, r *http.Request) {
	item, err := m.deps.Integration.GetBilibiliAPIAlertSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, item)
}

func (m *integrationModule) saveBilibiliAlertSetting(w http.ResponseWriter, r *http.Request) {
	var req store.BilibiliAPIAlertSetting
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	updated, err := m.deps.Integration.SaveBilibiliAPIAlertSetting(r.Context(), req)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, updated)
}

func (m *integrationModule) checkBilibiliAPIAlert(w http.ResponseWriter, r *http.Request) {
	setting, err := m.deps.Integration.GetBilibiliAPIAlertSetting(r.Context())
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if setting == nil || !setting.Enabled {
		httpapi.OK(w, map[string]any{
			"triggered": false,
			"reason":    "alert setting disabled",
		})
		return
	}

	now := time.Now()
	if setting.LastAlertAt != nil && setting.CooldownMinutes > 0 {
		cooldownUntil := setting.LastAlertAt.Add(time.Duration(setting.CooldownMinutes) * time.Minute)
		if now.Before(cooldownUntil) {
			httpapi.OK(w, map[string]any{
				"triggered":      false,
				"reason":         "cooldown",
				"cooldownUntil":  cooldownUntil.Format(time.RFC3339),
				"lastAlertAt":    setting.LastAlertAt,
				"cooldownMinute": setting.CooldownMinutes,
			})
			return
		}
	}

	since := now.Add(-time.Duration(setting.WindowMinutes) * time.Minute)
	total, err := m.deps.Integration.CountLiveEventsByTypeSince(r.Context(), "bilibili.api.error", since)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	if int(total) < setting.Threshold {
		httpapi.OK(w, map[string]any{
			"triggered": false,
			"reason":    "threshold not reached",
			"count":     total,
			"threshold": setting.Threshold,
		})
		return
	}
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue, intsvc.FeatureWebhook) {
		return
	}

	events, err := m.deps.Integration.ListLiveEventsByTypeSince(r.Context(), "bilibili.api.error", since, 300)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	parsed := parseBilibiliErrorEvents(events)
	categoryCount := map[string]int{}
	for _, item := range parsed {
		categoryCount[item.Category]++
	}
	categoryList := make([]map[string]any, 0, len(categoryCount))
	for key, count := range categoryCount {
		categoryList = append(categoryList, map[string]any{
			"category": key,
			"count":    count,
		})
	}
	sort.Slice(categoryList, func(i, j int) bool {
		if categoryList[i]["count"].(int) == categoryList[j]["count"].(int) {
			return categoryList[i]["category"].(string) < categoryList[j]["category"].(string)
		}
		return categoryList[i]["count"].(int) > categoryList[j]["count"].(int)
	})

	webhooks, err := m.deps.Integration.ListWebhooks(r.Context(), 1000, 0)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	eventType := strings.TrimSpace(setting.WebhookEvent)
	if eventType == "" {
		eventType = "bilibili.api.alert"
	}

	payload := map[string]any{
		"eventType":     eventType,
		"time":          now.Format(time.RFC3339),
		"windowMinutes": setting.WindowMinutes,
		"threshold":     setting.Threshold,
		"errorCount":    total,
		"categoryTop":   categoryList,
		"samples":       topBilibiliErrorSamples(parsed, 10),
	}

	success := make([]map[string]any, 0)
	failed := make([]map[string]any, 0)
	for _, item := range webhooks {
		if !item.Enabled {
			continue
		}
		taskID, callErr := m.deps.Integration.EnqueueWebhookTask(r.Context(), item, eventType, payload, 3)
		if callErr != nil {
			failed = append(failed, map[string]any{
				"id":    item.ID,
				"name":  item.Name,
				"error": callErr.Error(),
			})
			continue
		}
		success = append(success, map[string]any{
			"id":     item.ID,
			"name":   item.Name,
			"taskId": taskID,
		})
	}

	_ = m.deps.Integration.MarkBilibiliAPIAlertSent(r.Context(), setting.ID, now)
	alertEventBody, _ := json.Marshal(map[string]any{
		"count":      total,
		"threshold":  setting.Threshold,
		"window":     setting.WindowMinutes,
		"success":    len(success),
		"failed":     len(failed),
		"webhookEvt": eventType,
	})
	_ = m.deps.Integration.SaveLiveEvent(r.Context(), "bilibili.api.alert.sent", string(alertEventBody))

	httpapi.OK(w, map[string]any{
		"triggered":    true,
		"mode":         "async-queue",
		"eventType":    eventType,
		"errorCount":   total,
		"threshold":    setting.Threshold,
		"windowMins":   setting.WindowMinutes,
		"success":      success,
		"failed":       failed,
		"alertSetting": setting,
	})
}

func (m *integrationModule) loadBilibiliErrorEvents(ctx context.Context, limit int) ([]bilibiliErrorEvent, error) {
	items, err := m.deps.Integration.ListLiveEventsByType(ctx, "bilibili.api.error", limit)
	if err != nil {
		return nil, err
	}
	return parseBilibiliErrorEvents(items), nil
}

func (m *integrationModule) ptzDiscover(w http.ResponseWriter, r *http.Request) {
	timeoutMS := parseIntOrDefault(r.URL.Query().Get("timeoutMs"), 2500)
	if timeoutMS < 800 {
		timeoutMS = 800
	}
	if timeoutMS > 15000 {
		timeoutMS = 15000
	}
	activeScan := parseBoolQueryOrDefault(r.URL.Query().Get("activeScan"), true)
	ports := parsePortListQuery(r.URL.Query().Get("ports"))
	hostLimit := parseIntOrDefault(r.URL.Query().Get("hostLimit"), 512)
	if hostLimit <= 0 {
		hostLimit = 128
	}
	if hostLimit > 4096 {
		hostLimit = 4096
	}
	items, err := m.deps.ONVIF.DiscoverWithOptions(r.Context(), onvif.DiscoverOptions{
		Timeout:        time.Duration(timeoutMS) * time.Millisecond,
		ActiveScan:     activeScan,
		Ports:          ports,
		MaxHosts:       hostLimit,
		MaxConcurrency: 48,
		RequestTimeout: 700 * time.Millisecond,
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	payload := map[string]any{
		"timeoutMs":  timeoutMS,
		"activeScan": activeScan,
		"hostLimit":  hostLimit,
		"count":      len(items),
		"items":      items,
	}
	if len(ports) > 0 {
		payload["ports"] = ports
	}
	if len(items) == 0 {
		payload["hints"] = []string{
			"确认摄像头已在厂商后台开启 ONVIF，并允许局域网发现",
			"优先在同网段有线网络测试，避免访客网络/VLAN 隔离",
			"可尝试加长超时：/api/v1/ptz/discover?timeoutMs=12000&activeScan=1&ports=80,2020,8899",
			"部分 V380 机型可能仅支持 RTSP，不一定实现标准 ONVIF WS-Discovery",
		}
	}
	httpapi.OK(w, payload)
}

func (m *integrationModule) ptzCommand(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint     string  `json:"endpoint"`
		Username     string  `json:"username"`
		Password     string  `json:"password"`
		ProfileToken string  `json:"profileToken"`
		PresetToken  string  `json:"presetToken"`
		Action       string  `json:"action"`
		Direction    string  `json:"direction"`
		Speed        float64 `json:"speed"`
		DurationMS   int     `json:"durationMs"`
		Pan          float64 `json:"pan"`
		Tilt         float64 `json:"tilt"`
		Zoom         float64 `json:"zoom"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		if pushSetting, err := m.deps.Store.GetPushSetting(r.Context()); err == nil {
			req.Endpoint = pushSetting.ONVIFEndpoint
			req.Username = pushSetting.ONVIFUsername
			req.Password = pushSetting.ONVIFPassword
			req.ProfileToken = pushSetting.ONVIFProfileToken
		}
	}
	result, err := m.deps.ONVIF.ExecuteCommand(r.Context(), onvif.CommandRequest{
		Endpoint:     req.Endpoint,
		Username:     req.Username,
		Password:     req.Password,
		ProfileToken: req.ProfileToken,
		PresetToken:  req.PresetToken,
		Action:       req.Action,
		Direction:    req.Direction,
		Speed:        req.Speed,
		DurationMS:   req.DurationMS,
		Pan:          req.Pan,
		Tilt:         req.Tilt,
		Zoom:         req.Zoom,
	})
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *integrationModule) ptzCapabilities(w http.ResponseWriter, r *http.Request) {
	endpoint := strings.TrimSpace(r.URL.Query().Get("endpoint"))
	username := strings.TrimSpace(r.URL.Query().Get("username"))
	password := r.URL.Query().Get("password")
	if endpoint == "" {
		if pushSetting, err := m.deps.Store.GetPushSetting(r.Context()); err == nil {
			endpoint = pushSetting.ONVIFEndpoint
			username = pushSetting.ONVIFUsername
			password = pushSetting.ONVIFPassword
		}
	}
	caps, err := m.deps.ONVIF.GetCapabilities(r.Context(), endpoint, username, password)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, caps)
}

func (m *integrationModule) ptzProfiles(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		if pushSetting, err := m.deps.Store.GetPushSetting(r.Context()); err == nil {
			req.Endpoint = pushSetting.ONVIFEndpoint
			req.Username = pushSetting.ONVIFUsername
			req.Password = pushSetting.ONVIFPassword
		}
	}
	profiles, err := m.deps.ONVIF.GetProfiles(r.Context(), req.Endpoint, req.Username, req.Password)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, profiles)
}

func (m *integrationModule) testWebhook(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue, intsvc.FeatureWebhook) {
		return
	}
	var req struct {
		ID        int64  `json:"id"`
		EventType string `json:"eventType"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	webhooks, err := m.deps.Integration.ListWebhooks(r.Context(), 1000, 0)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	var matched *store.WebhookSetting
	for _, item := range webhooks {
		if item.ID == req.ID {
			c := item
			matched = &c
			break
		}
	}
	if matched == nil {
		httpapi.Error(w, -1, "webhook not found", http.StatusOK)
		return
	}
	payload := map[string]any{
		"eventType": defaultString(req.EventType, "webhook.test"),
		"time":      time.Now().Format(time.RFC3339),
		"source":    "gover",
		"data": map[string]any{
			"id":   matched.ID,
			"name": matched.Name,
		},
	}
	taskID, err := m.deps.Integration.EnqueueWebhookTask(r.Context(), *matched, defaultString(req.EventType, "webhook.test"), payload, 3)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"message": "queued",
		"taskId":  taskID,
	})
}

func (m *integrationModule) notifyWebhooks(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue, intsvc.FeatureWebhook) {
		return
	}
	var req struct {
		EventType string          `json:"eventType"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	eventType := defaultString(req.EventType, "notify")
	_ = m.deps.Integration.SaveLiveEvent(r.Context(), eventType, string(req.Payload))

	webhooks, err := m.deps.Integration.ListWebhooks(r.Context(), 1000, 0)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}

	success := make([]map[string]any, 0)
	failed := make([]map[string]any, 0)
	enabledCount := 0
	for _, item := range webhooks {
		if !item.Enabled {
			continue
		}
		enabledCount++
		payload := map[string]any{
			"eventType": eventType,
			"time":      time.Now().Format(time.RFC3339),
			"source":    "gover",
			"payload":   json.RawMessage(req.Payload),
		}
		taskID, callErr := m.deps.Integration.EnqueueWebhookTask(r.Context(), item, eventType, payload, 3)
		if callErr != nil {
			failed = append(failed, map[string]any{
				"id":    item.ID,
				"name":  item.Name,
				"error": callErr.Error(),
			})
			errPayload, _ := json.Marshal(map[string]any{
				"eventType": eventType,
				"webhookId": item.ID,
				"name":      item.Name,
				"error":     callErr.Error(),
			})
			_ = m.deps.Integration.SaveLiveEvent(r.Context(), "webhook.delivery.error", string(errPayload))
			continue
		}
		success = append(success, map[string]any{
			"id":     item.ID,
			"name":   item.Name,
			"taskId": taskID,
		})
	}

	httpapi.OK(w, map[string]any{
		"eventType": eventType,
		"enabled":   enabledCount,
		"mode":      "async-queue",
		"success":   success,
		"failed":    failed,
	})
}

func (m *integrationModule) botCommand(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue, intsvc.FeatureBot) {
		return
	}
	var req struct {
		Provider string          `json:"provider"`
		Command  string          `json:"command"`
		Params   json.RawMessage `json:"params"`
	}
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	command := strings.ToLower(strings.TrimSpace(req.Command))
	switch command {
	case "start_live", "stop_live", "ptz", "send_danmaku", "provider_notify":
	default:
		httpapi.Error(w, -1, "unsupported bot command", http.StatusOK)
		return
	}
	_ = m.deps.Integration.SaveLiveEvent(r.Context(), "bot.command", string(req.Params))
	taskID, err := m.deps.Integration.EnqueueBotTask(r.Context(), req.Provider, command, req.Params, 3)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"provider": req.Provider,
		"command":  command,
		"status":   "queued",
		"taskId":   taskID,
	})
}

func (m *integrationModule) providerInboundWebhook(w http.ResponseWriter, r *http.Request) {
	if !m.ensureFeaturesEnabled(w, r, intsvc.FeatureTaskQueue, intsvc.FeatureBot) {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "provider")))
	if provider == "" {
		httpapi.Error(w, -1, "provider is required", http.StatusBadRequest)
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body.Close()
	rawBody := bodyBytes
	if len(rawBody) == 0 {
		rawBody = []byte("{}")
	}

	authMode, err := m.verifyProviderInboundAuth(r, provider, rawBody)
	if err != nil {
		_ = m.deps.Integration.SaveLiveEvent(r.Context(), "provider.inbound.auth.error", fmt.Sprintf(`{"provider":"%s","error":%q}`, provider, err.Error()))
		httpapi.Error(w, -1, err.Error(), http.StatusUnauthorized)
		return
	}
	normalizedBody := bytes.TrimSpace(rawBody)
	if len(normalizedBody) == 0 {
		normalizedBody = []byte("{}")
	}

	payload := map[string]any{}
	if err := json.Unmarshal(normalizedBody, &payload); err != nil {
		httpapi.Error(w, -1, "invalid json payload", http.StatusBadRequest)
		return
	}
	command, params, parseErr := parseProviderInboundCommand(provider, payload)
	if parseErr != nil {
		httpapi.Error(w, -1, parseErr.Error(), http.StatusOK)
		return
	}
	command = strings.ToLower(strings.TrimSpace(command))
	switch command {
	case "start_live", "stop_live", "ptz", "send_danmaku", "provider_notify":
	default:
		httpapi.Error(w, -1, "unsupported inbound command", http.StatusOK)
		return
	}

	paramsBytes, err := json.Marshal(params)
	if err != nil {
		httpapi.Error(w, -1, "encode params failed: "+err.Error(), http.StatusOK)
		return
	}
	taskID, err := m.deps.Integration.EnqueueBotTask(r.Context(), provider, command, paramsBytes, 3)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	_ = m.deps.Integration.SaveLiveEvent(r.Context(), "provider.inbound.accepted", fmt.Sprintf(`{"provider":"%s","command":"%s","taskId":%d,"authMode":"%s"}`, provider, command, taskID, authMode))
	httpapi.OK(w, map[string]any{
		"provider": provider,
		"command":  command,
		"status":   "queued",
		"taskId":   taskID,
		"authMode": authMode,
	})
}

func (m *integrationModule) verifyProviderInboundAuth(r *http.Request, provider string, body []byte) (string, error) {
	if err := m.verifyProviderInboundWhitelist(r, provider); err != nil {
		return "", err
	}
	if mode, matched, err := m.verifyProviderOfficialAuth(r, provider, body); matched || err != nil {
		if err != nil {
			return "", err
		}
		return mode, nil
	}
	mode, err := m.verifyProviderInboundGoverHMAC(r, provider, body)
	if err != nil {
		return "", err
	}
	return mode, nil
}

func (m *integrationModule) verifyProviderInboundGoverHMAC(r *http.Request, provider string, body []byte) (string, error) {
	secret := m.resolveProviderInboundSecret(r.Context(), provider)
	if strings.TrimSpace(secret) == "" {
		return "", errors.New("provider inbound secret is empty, set api key first")
	}
	timestampText := strings.TrimSpace(r.Header.Get("X-Gover-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Gover-Nonce"))
	signature := strings.TrimSpace(r.Header.Get("X-Gover-Signature"))
	if timestampText == "" || nonce == "" || signature == "" {
		return "", errors.New("missing X-Gover-Timestamp/X-Gover-Nonce/X-Gover-Signature")
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return "", errors.New("invalid X-Gover-Timestamp")
	}
	now := time.Now().Unix()
	skew := int64(m.resolveProviderInboundSkew(r.Context()))
	if skew <= 0 {
		skew = 300
	}
	if absInt64(now-timestamp) > skew {
		return "", errors.New("inbound timestamp skew too large")
	}

	canonical := timestampText + "\n" + nonce + "\n" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(canonical))
	expected := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(signature)), []byte(strings.ToLower(expected))) != 1 {
		return "", errors.New("invalid inbound signature")
	}

	replayKey := provider + "|" + timestampText + "|" + nonce + "|" + strings.ToLower(signature)
	if !markInboundReplay(replayKey, time.Duration(skew*2)*time.Second) {
		return "", errors.New("replay detected")
	}
	return "gover_hmac", nil
}

func (m *integrationModule) verifyProviderOfficialAuth(r *http.Request, provider string, body []byte) (string, bool, error) {
	switch provider {
	case "telegram", "tg":
		return m.verifyTelegramOfficialAuth(r, body)
	case "dingtalk", "ding":
		return m.verifyDingTalkOfficialAuth(r, body)
	default:
		return "", false, nil
	}
}

func (m *integrationModule) verifyTelegramOfficialAuth(r *http.Request, body []byte) (string, bool, error) {
	secretHeader := strings.TrimSpace(r.Header.Get("X-Telegram-Bot-Api-Secret-Token"))
	if secretHeader == "" {
		return "", false, nil
	}
	expected := m.resolveAPIKeyByCandidates(r.Context(),
		"telegram_inbound_secret_token",
		"telegram_bot_secret_token",
		"telegram_inbound_secret",
	)
	if expected == "" {
		return "", true, errors.New("telegram official secret token header is present, but api key telegram_inbound_secret_token is empty")
	}
	if subtle.ConstantTimeCompare([]byte(secretHeader), []byte(expected)) != 1 {
		return "", true, errors.New("invalid telegram secret token")
	}
	if updateID := extractTelegramUpdateID(body); updateID != "" {
		replayKey := "telegram|update_id|" + updateID
		if !markInboundReplay(replayKey, 2*time.Hour) {
			return "", true, errors.New("telegram replay detected by update_id")
		}
	}
	return "telegram_secret_token", true, nil
}

func (m *integrationModule) verifyDingTalkOfficialAuth(r *http.Request, body []byte) (string, bool, error) {
	query := r.URL.Query()
	timestampText := firstNonEmpty(
		strings.TrimSpace(query.Get("timestamp")),
		strings.TrimSpace(r.Header.Get("timestamp")),
		strings.TrimSpace(r.Header.Get("X-Timestamp")),
	)
	signature := firstNonEmpty(
		strings.TrimSpace(query.Get("sign")),
		strings.TrimSpace(r.Header.Get("sign")),
		strings.TrimSpace(r.Header.Get("X-DingTalk-Sign")),
	)
	if timestampText != "" || signature != "" {
		secret := m.resolveAPIKeyByCandidates(r.Context(),
			"dingtalk_inbound_sign_secret",
			"dingtalk_sign_secret",
			"dingtalk_inbound_secret",
		)
		if strings.TrimSpace(secret) == "" {
			return "", true, errors.New("dingtalk sign/timestamp found, but dingtalk_inbound_sign_secret is empty")
		}
		if timestampText == "" || signature == "" {
			return "", true, errors.New("dingtalk official signature missing timestamp/sign")
		}
		timestamp, err := strconv.ParseInt(timestampText, 10, 64)
		if err != nil {
			return "", true, errors.New("invalid dingtalk timestamp")
		}
		skew := int64(m.resolveProviderInboundSkew(r.Context()))
		if skew <= 0 {
			skew = 300
		}
		if absInt64(time.Now().Unix()-timestamp/1000) > skew && absInt64(time.Now().Unix()-timestamp) > skew {
			return "", true, errors.New("dingtalk timestamp skew too large")
		}
		canonical := timestampText + "\n" + secret
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
		if !compareMaybeURLDecodedSignature(signature, expected) {
			return "", true, errors.New("invalid dingtalk signature")
		}
		replayKey := "dingtalk|sign|" + timestampText + "|" + strings.ToLower(signature)
		if !markInboundReplay(replayKey, time.Duration(skew*2)*time.Second) {
			return "", true, errors.New("dingtalk replay detected")
		}
		return "dingtalk_signature", true, nil
	}

	callbackSign := firstNonEmpty(
		strings.TrimSpace(query.Get("msg_signature")),
		strings.TrimSpace(query.Get("signature")),
		strings.TrimSpace(r.Header.Get("X-DingTalk-Signature")),
	)
	if callbackSign == "" {
		return "", false, nil
	}
	timestampText = firstNonEmpty(
		timestampText,
		strings.TrimSpace(query.Get("timestamp")),
		strings.TrimSpace(r.Header.Get("X-DingTalk-Timestamp")),
	)
	nonce := firstNonEmpty(
		strings.TrimSpace(query.Get("nonce")),
		strings.TrimSpace(r.Header.Get("X-DingTalk-Nonce")),
	)
	encrypt := extractDingTalkEncryptField(body)
	token := m.resolveAPIKeyByCandidates(r.Context(),
		"dingtalk_callback_token",
		"dingtalk_inbound_callback_token",
	)
	if token == "" {
		return "", true, errors.New("dingtalk callback signature present, but dingtalk_callback_token is empty")
	}
	if timestampText == "" || nonce == "" || encrypt == "" {
		return "", true, errors.New("dingtalk callback signature requires timestamp/nonce/encrypt")
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return "", true, errors.New("invalid dingtalk callback timestamp")
	}
	skew := int64(m.resolveProviderInboundSkew(r.Context()))
	if skew <= 0 {
		skew = 300
	}
	if absInt64(time.Now().Unix()-timestamp/1000) > skew && absInt64(time.Now().Unix()-timestamp) > skew {
		return "", true, errors.New("dingtalk callback timestamp skew too large")
	}
	expected := computeDingTalkCallbackSignature(token, timestampText, nonce, encrypt)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(callbackSign)), []byte(strings.ToLower(expected))) != 1 {
		return "", true, errors.New("invalid dingtalk callback signature")
	}
	replayKey := "dingtalk|callback|" + timestampText + "|" + nonce + "|" + strings.ToLower(callbackSign)
	if !markInboundReplay(replayKey, time.Duration(skew*2)*time.Second) {
		return "", true, errors.New("dingtalk callback replay detected")
	}
	return "dingtalk_callback_signature", true, nil
}

func compareMaybeURLDecodedSignature(signature string, expected string) bool {
	signature = strings.TrimSpace(signature)
	expected = strings.TrimSpace(expected)
	if signature == "" || expected == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(signature), []byte(expected)) == 1 {
		return true
	}
	if decoded, err := url.QueryUnescape(signature); err == nil {
		if subtle.ConstantTimeCompare([]byte(decoded), []byte(expected)) == 1 {
			return true
		}
	}
	if decodedExpected, err := url.QueryUnescape(expected); err == nil {
		if subtle.ConstantTimeCompare([]byte(signature), []byte(decodedExpected)) == 1 {
			return true
		}
	}
	return false
}

func computeDingTalkCallbackSignature(token string, timestamp string, nonce string, encrypt string) string {
	items := []string{token, timestamp, nonce, encrypt}
	sort.Strings(items)
	raw := strings.Join(items, "")
	sum := sha1.Sum([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func extractDingTalkEncryptField(body []byte) string {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(asString(payload["encrypt"]))
}

func extractTelegramUpdateID(body []byte) string {
	payload := map[string]any{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if value := strings.TrimSpace(asString(payload["update_id"])); value != "" {
		return value
	}
	return strings.TrimSpace(asString(payload["updateId"]))
}

func (m *integrationModule) verifyProviderInboundWhitelist(r *http.Request, provider string) error {
	raw := m.resolveProviderInboundWhitelist(r.Context(), provider)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	remoteIP, err := extractInboundRemoteIP(r)
	if err != nil {
		return err
	}
	matched, parseErr := matchInboundWhitelist(remoteIP, raw)
	if parseErr != nil {
		return parseErr
	}
	if !matched {
		return errors.New("provider inbound source ip is not in whitelist")
	}
	return nil
}

func (m *integrationModule) resolveProviderInboundWhitelist(ctx context.Context, provider string) string {
	return m.resolveAPIKeyByCandidates(ctx,
		provider+"_inbound_whitelist",
		provider+"_inbound_allowlist",
		"provider_inbound_"+provider+"_whitelist",
		"provider_inbound_"+provider+"_allowlist",
		"provider_inbound_whitelist",
		"provider_inbound_allowlist",
	)
}

func extractInboundRemoteIP(r *http.Request) (netip.Addr, error) {
	candidates := []string{
		strings.TrimSpace(r.Header.Get("X-Forwarded-For")),
		strings.TrimSpace(r.Header.Get("X-Real-IP")),
		strings.TrimSpace(r.RemoteAddr),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if strings.Contains(candidate, ",") {
			candidate = strings.TrimSpace(strings.Split(candidate, ",")[0])
		}
		addr, err := parseNetIP(candidate)
		if err == nil {
			return addr, nil
		}
	}
	return netip.Addr{}, errors.New("failed to parse inbound remote ip")
}

func parseNetIP(value string) (netip.Addr, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return netip.Addr{}, errors.New("empty ip")
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, err
	}
	return addr.Unmap(), nil
}

func matchInboundWhitelist(ip netip.Addr, raw string) (bool, error) {
	validRuleCount := 0
	for _, rule := range splitWhitelistRules(raw) {
		if strings.Contains(rule, "/") {
			prefix, err := netip.ParsePrefix(rule)
			if err != nil {
				continue
			}
			validRuleCount++
			if prefix.Contains(ip) {
				return true, nil
			}
			continue
		}
		addr, err := netip.ParseAddr(rule)
		if err != nil {
			continue
		}
		validRuleCount++
		if addr.Unmap() == ip {
			return true, nil
		}
	}
	if validRuleCount == 0 {
		return false, errors.New("provider inbound whitelist is configured but no valid ip/cidr rule found")
	}
	return false, nil
}

func splitWhitelistRules(raw string) []string {
	raw = strings.ReplaceAll(raw, "\n", ",")
	raw = strings.ReplaceAll(raw, ";", ",")
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, item := range parts {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		result = append(result, item)
	}
	return result
}

func parseBoolQueryOrDefault(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func parsePortListQuery(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	replacer := strings.NewReplacer("\n", ",", ";", ",", "|", ",", " ", ",")
	parts := strings.Split(replacer.Replace(raw), ",")
	seen := map[int]struct{}{}
	result := make([]int, 0, len(parts))
	for _, item := range parts {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		port, err := strconv.Atoi(item)
		if err != nil || port <= 0 || port > 65535 {
			continue
		}
		if _, ok := seen[port]; ok {
			continue
		}
		seen[port] = struct{}{}
		result = append(result, port)
	}
	sort.Ints(result)
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (m *integrationModule) resolveAPIKeyByCandidates(ctx context.Context, names ...string) string {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		item, err := m.deps.Store.GetAPIKeyByName(ctx, name)
		if err != nil || item == nil {
			continue
		}
		if strings.TrimSpace(item.APIKey) != "" {
			return strings.TrimSpace(item.APIKey)
		}
	}
	return ""
}

func (m *integrationModule) resolveProviderInboundSecret(ctx context.Context, provider string) string {
	return m.resolveAPIKeyByCandidates(ctx,
		provider+"_inbound_secret",
		"provider_inbound_"+provider,
		"provider_inbound_secret",
	)
}

func (m *integrationModule) resolveProviderInboundSkew(ctx context.Context) int {
	item, err := m.deps.Store.GetAPIKeyByName(ctx, "provider_inbound_skew_sec")
	if err != nil || item == nil {
		return 300
	}
	value, parseErr := strconv.Atoi(strings.TrimSpace(item.APIKey))
	if parseErr != nil {
		return 300
	}
	if value < 60 {
		return 60
	}
	if value > 1800 {
		return 1800
	}
	return value
}

func markInboundReplay(key string, ttl time.Duration) bool {
	now := time.Now()
	expireAt := now.Add(ttl)
	providerInboundReplayCache.mu.Lock()
	defer providerInboundReplayCache.mu.Unlock()
	for itemKey, itemExpireAt := range providerInboundReplayCache.items {
		if itemExpireAt.Before(now) {
			delete(providerInboundReplayCache.items, itemKey)
		}
	}
	if cachedExpireAt, exists := providerInboundReplayCache.items[key]; exists && cachedExpireAt.After(now) {
		return false
	}
	providerInboundReplayCache.items[key] = expireAt
	return true
}

func parseProviderInboundCommand(provider string, payload map[string]any) (string, map[string]any, error) {
	command := strings.TrimSpace(asString(payload["command"]))
	params := map[string]any{}
	if rawParams, ok := payload["params"].(map[string]any); ok {
		for key, value := range rawParams {
			params[key] = value
		}
	}
	if command != "" {
		return command, params, nil
	}

	text := extractProviderText(provider, payload)
	if strings.TrimSpace(text) == "" {
		return "", nil, errors.New("command/text is required")
	}
	return parseTextCommand(text)
}

func extractProviderText(provider string, payload map[string]any) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "telegram", "tg":
		if msg, ok := payload["message"].(map[string]any); ok {
			if text := strings.TrimSpace(asString(msg["text"])); text != "" {
				return text
			}
		}
		if msg, ok := payload["edited_message"].(map[string]any); ok {
			if text := strings.TrimSpace(asString(msg["text"])); text != "" {
				return text
			}
		}
		if callback, ok := payload["callback_query"].(map[string]any); ok {
			if text := strings.TrimSpace(asString(callback["data"])); text != "" {
				return text
			}
		}
	case "dingtalk", "ding":
		if textObj, ok := payload["text"].(map[string]any); ok {
			if text := strings.TrimSpace(asString(textObj["content"])); text != "" {
				return text
			}
		}
		if text := strings.TrimSpace(asString(payload["content"])); text != "" {
			return text
		}
	}
	if text := strings.TrimSpace(asString(payload["text"])); text != "" {
		return text
	}
	return strings.TrimSpace(asString(payload["message"]))
}

func parseTextCommand(text string) (string, map[string]any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(strings.ToLower(text), "/gover") {
		text = strings.TrimSpace(text[len("/gover"):])
	}
	if strings.HasPrefix(strings.ToLower(text), "gover") {
		text = strings.TrimSpace(text[len("gover"):])
	}
	if text == "" {
		return "", nil, errors.New("empty command")
	}
	tokens := strings.Fields(text)
	if len(tokens) == 0 {
		return "", nil, errors.New("empty command")
	}
	command := strings.ToLower(strings.TrimSpace(tokens[0]))
	params := map[string]any{}
	switch command {
	case "start_live", "stop_live":
		return command, params, nil
	case "ptz":
		action := "stop"
		if len(tokens) >= 2 {
			action = strings.ToLower(strings.TrimSpace(tokens[1]))
		}
		speed := 0.3
		if len(tokens) >= 3 {
			if parsed, err := strconv.ParseFloat(tokens[2], 64); err == nil && parsed > 0 {
				speed = parsed
			}
		}
		params["action"] = action
		params["speed"] = speed
		return command, params, nil
	case "send_danmaku":
		if len(tokens) < 2 {
			return "", nil, errors.New("send_danmaku requires message")
		}
		if len(tokens) >= 3 {
			if roomID, err := strconv.ParseInt(tokens[1], 10, 64); err == nil && roomID > 0 {
				params["roomId"] = roomID
				params["message"] = strings.TrimSpace(strings.Join(tokens[2:], " "))
				return command, params, nil
			}
		}
		params["message"] = strings.TrimSpace(strings.Join(tokens[1:], " "))
		return command, params, nil
	case "provider_notify":
		params["content"] = strings.TrimSpace(strings.TrimPrefix(text, tokens[0]))
		return command, params, nil
	default:
		return "", nil, errors.New("unsupported inbound command: " + command)
	}
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func (m *integrationModule) executeRule(ctx context.Context, rule store.DanmakuPTZRule, req dispatchDanmakuRequest, pushSetting *store.PushSetting) (map[string]any, error) {
	action := strings.ToLower(strings.TrimSpace(rule.Action))
	if action == "" {
		action = "ptz"
	}
	switch action {
	case "ptz":
		if pushSetting == nil {
			return nil, errors.New("ptz rule failed: push setting not found")
		}
		ptzAction := normalizePTZAction(rule.PTZDirection)
		speed := float64(rule.PTZSpeed) / 10
		if speed <= 0 {
			speed = 0.3
		}
		if speed > 1 {
			speed = 1
		}
		result, err := m.deps.ONVIF.ExecuteCommand(ctx, onvif.CommandRequest{
			Endpoint:     pushSetting.ONVIFEndpoint,
			Username:     pushSetting.ONVIFUsername,
			Password:     pushSetting.ONVIFPassword,
			ProfileToken: pushSetting.ONVIFProfileToken,
			Action:       ptzAction,
			Speed:        speed,
			DurationMS:   700,
		})
		if err != nil {
			return nil, err
		}
		return result, nil
	case "start_live":
		if err := m.deps.Stream.Start(ctx, false); err != nil {
			return nil, err
		}
		return map[string]any{"started": true}, nil
	case "stop_live":
		_ = m.deps.Stream.Stop(ctx)
		if room, err := m.deps.Store.GetLiveSetting(ctx); err == nil && room.RoomID > 0 {
			_ = m.deps.Bilibili.StopLive(ctx, room.RoomID)
		}
		return map[string]any{"stopped": true}, nil
	case "webhook":
		payload := map[string]any{
			"eventType": "danmaku.rule.webhook",
			"time":      time.Now().Format(time.RFC3339),
			"source":    defaultString(req.Source, "manual"),
			"data": map[string]any{
				"roomId":  req.RoomID,
				"uid":     req.UID,
				"uname":   req.Uname,
				"content": req.Content,
				"ruleId":  rule.ID,
				"keyword": rule.Keyword,
			},
		}
		webhooks, err := m.deps.Integration.ListWebhooks(ctx, 1000, 0)
		if err != nil {
			return nil, err
		}
		successCount := 0
		failed := make([]string, 0)
		for _, item := range webhooks {
			if !item.Enabled {
				continue
			}
			if _, callErr := m.dispatchWebhook(ctx, item, "danmaku.rule.webhook", payload, 2); callErr != nil {
				failed = append(failed, item.Name+": "+callErr.Error())
				continue
			}
			successCount++
		}
		return map[string]any{
			"successCount": successCount,
			"failed":       failed,
		}, nil
	default:
		return nil, errors.New("unsupported rule action: " + action)
	}
}

func (m *integrationModule) dispatchWebhook(ctx context.Context, target store.WebhookSetting, eventType string, payload any, maxAttempts int) (*webhookSendResult, error) {
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var lastResult *webhookSendResult
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		result, err := sendWebhook(ctx, target, payload)
		lastResult = result
		lastErr = err

		logEntry := store.WebhookDeliveryLog{
			WebhookName: target.Name,
			EventType:   eventType,
			Attempt:     attempt,
		}
		if target.ID > 0 {
			id := target.ID
			logEntry.WebhookID = &id
		}
		if result != nil {
			logEntry.RequestBody = result.RequestBody
			logEntry.ResponseStatus = result.ResponseCode
			logEntry.ResponseBody = result.ResponseBody
			logEntry.DurationMS = result.DurationMS
		}
		if err != nil {
			logEntry.ErrorMessage = err.Error()
			logEntry.Success = false
		} else {
			logEntry.Success = true
		}
		if saveErr := m.deps.Integration.CreateWebhookDeliveryLog(ctx, logEntry); saveErr != nil {
			log.Printf("[integration][warn] save webhook delivery log failed: %v", saveErr)
		}

		if err == nil {
			return result, nil
		}
		if attempt == maxAttempts || !shouldRetryWebhook(result, err) {
			break
		}
		time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("webhook dispatch failed with unknown error")
	}
	return lastResult, lastErr
}

func sendWebhook(ctx context.Context, target store.WebhookSetting, payload any) (*webhookSendResult, error) {
	if strings.TrimSpace(target.URL) == "" {
		return nil, errors.New("webhook url is empty")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gover-webhook/1.0")
	if strings.TrimSpace(target.Secret) != "" {
		mac := hmac.New(sha256.New, []byte(target.Secret))
		_, _ = mac.Write(data)
		req.Header.Set("X-Gover-Signature", hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := webhookHTTPClient.Do(req)
	if err != nil {
		return &webhookSendResult{
			RequestBody: string(data),
			DurationMS:  time.Since(start).Milliseconds(),
		}, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	result := &webhookSendResult{
		RequestBody:  string(data),
		ResponseBody: strings.TrimSpace(string(bodyBytes)),
		ResponseCode: resp.StatusCode,
		DurationMS:   time.Since(start).Milliseconds(),
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, errors.New("webhook call failed status=" + resp.Status)
	}
	return result, nil
}

func shouldRetryWebhook(result *webhookSendResult, err error) bool {
	if err == nil {
		return false
	}
	if result != nil && (result.ResponseCode == http.StatusTooManyRequests || result.ResponseCode >= 500) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "timeout") || strings.Contains(lower, "connection reset") || strings.Contains(lower, "temporarily")
}

func normalizePTZAction(direction string) string {
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "left":
		return "left"
	case "right":
		return "right"
	case "up":
		return "up"
	case "down":
		return "down"
	case "zoom_in":
		return "zoom_in"
	case "zoom_out":
		return "zoom_out"
	case "center", "home":
		return "home"
	default:
		return "stop"
	}
}

func parseBilibiliErrorEvents(items []store.LiveEvent) []bilibiliErrorEvent {
	result := make([]bilibiliErrorEvent, 0, len(items))
	for _, item := range items {
		var payload struct {
			Endpoint  string `json:"endpoint"`
			Method    string `json:"method"`
			Detail    string `json:"detail"`
			Attempt   int    `json:"attempt"`
			Retryable bool   `json:"retryable"`
		}
		if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil {
			continue
		}
		category, advice := classifyBilibiliError(payload.Endpoint, payload.Detail)
		event := bilibiliErrorEvent{
			ID:        item.ID,
			Endpoint:  strings.TrimSpace(payload.Endpoint),
			Method:    strings.ToUpper(strings.TrimSpace(payload.Method)),
			Detail:    strings.TrimSpace(payload.Detail),
			Attempt:   payload.Attempt,
			Retryable: payload.Retryable,
			Category:  category,
			Advice:    advice,
			CreatedAt: item.CreatedAt,
		}
		if event.Method == "" {
			event.Method = "UNKNOWN"
		}
		if event.Endpoint == "" {
			event.Endpoint = "unknown"
		}
		result = append(result, event)
	}
	return result
}

func classifyBilibiliError(endpoint string, detail string) (string, string) {
	lowerDetail := strings.ToLower(strings.TrimSpace(detail))
	lowerEndpoint := strings.ToLower(strings.TrimSpace(endpoint))

	switch {
	case strings.Contains(lowerDetail, "cookie is empty"),
		strings.Contains(lowerDetail, "missing bili_jct"),
		strings.Contains(lowerDetail, "is not logged in"),
		strings.Contains(lowerDetail, "code=-101"),
		strings.Contains(lowerDetail, "code=61000"),
		strings.Contains(lowerDetail, "code=65530"),
		strings.Contains(lowerDetail, "csrf"):
		return "auth_or_cookie_invalid", "检查 SESSDATA/bili_jct/refresh_token 是否过期并执行 Cookie 刷新后重试"
	case strings.Contains(lowerDetail, "http status 412"),
		strings.Contains(lowerDetail, "http status 403"),
		strings.Contains(lowerDetail, "forbidden"),
		strings.Contains(lowerDetail, "risk"),
		strings.Contains(lowerDetail, "风控"):
		return "risk_control_or_permission", "可能触发风控或权限不足，建议降低调用频率并确认账号权限"
	case strings.Contains(lowerDetail, "http status 429"),
		strings.Contains(lowerDetail, "too many requests"),
		strings.Contains(lowerDetail, "操作太频繁"),
		strings.Contains(lowerDetail, "code=-1"):
		return "rate_limit", "触发限流，请增加重试间隔并启用告警观察"
	case strings.Contains(lowerDetail, "timeout"),
		strings.Contains(lowerDetail, "connection reset"),
		strings.Contains(lowerDetail, "temporarily"),
		strings.Contains(lowerDetail, "eof"),
		strings.Contains(lowerDetail, "http status 5"):
		return "network_or_server_instability", "网络或服务端波动，已自动重试；建议观察错误频率是否持续升高"
	case strings.Contains(lowerDetail, "decode response failed"),
		strings.Contains(lowerDetail, "unsupported bilibili response shape"),
		strings.Contains(lowerDetail, "invalid qrcode response"):
		return "response_schema_changed", "疑似 Bilibili 接口返回结构变化，需更新解析逻辑"
	case strings.Contains(lowerEndpoint, "startlive") && strings.Contains(lowerDetail, "need face auth"):
		return "face_auth_required", "账号需要人脸认证后才能开播，请先在 Bilibili 完成人脸验证"
	case strings.Contains(lowerEndpoint, "area/getlist"),
		strings.Contains(lowerDetail, "60009"):
		return "area_or_business_rule_changed", "分区规则可能变化，请刷新分区并确认 area_id 是否有效"
	default:
		return "unknown", "未知错误，建议抓取当次请求与响应并在控制台查看 bilibili.api.error 详情"
	}
}

func topBilibiliErrorSamples(items []bilibiliErrorEvent, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	if len(items) < limit {
		limit = len(items)
	}
	result := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		item := items[i]
		detail := item.Detail
		if len(detail) > 240 {
			detail = detail[:240] + "...(truncated)"
		}
		result = append(result, map[string]any{
			"id":        item.ID,
			"endpoint":  item.Endpoint,
			"method":    item.Method,
			"category":  item.Category,
			"advice":    item.Advice,
			"detail":    detail,
			"createdAt": item.CreatedAt.Format(time.RFC3339),
		})
	}
	return result
}

func defaultString(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func asString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}
