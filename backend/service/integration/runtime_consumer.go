package integration

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/gorilla/websocket"

	"bilibililivetools/gover/backend/store"
)

var danmakuConsumerHTTPClient = &http.Client{Timeout: 15 * time.Second}

const (
	danmakuProviderHTTPPolling       = "http_polling"
	danmakuProviderBilibiliMsgStream = "bilibili_message_stream"
	defaultDanmuInfoEndpoint         = "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo"
	defaultBilibiliNavEndpoint       = "https://api.bilibili.com/x/web-interface/nav"
)

var mixinKeyEncTable = []int{
	46, 47, 18, 2, 53, 8, 23, 32, 15, 50, 10, 31, 58, 3, 45, 35,
	27, 43, 5, 49, 33, 9, 42, 19, 29, 28, 14, 39, 12, 38, 41, 13,
	37, 48, 7, 16, 24, 55, 40, 61, 26, 17, 0, 1, 60, 51, 30, 4,
	22, 25, 54, 21, 56, 59, 6, 63, 57, 62, 11, 36, 20, 34, 44, 52,
}

type bilibiliMessageStreamConfig struct {
	RoomID            int64             `json:"roomId"`
	TokenEndpoint     string            `json:"tokenEndpoint"`
	UseWBI            bool              `json:"useWbi"`
	UseCookie         bool              `json:"useCookie"`
	UID               int64             `json:"uid"`
	Protover          int               `json:"protover"`
	Platform          string            `json:"platform"`
	Type              int               `json:"type"`
	WebLocation       string            `json:"webLocation"`
	WSHost            string            `json:"wsHost"`
	WSPort            int               `json:"wsPort"`
	WSPath            string            `json:"wsPath"`
	PreferWSS         bool              `json:"preferWss"`
	ConnectTimeoutSec int               `json:"connectTimeoutSec"`
	ReadWindowSec     int               `json:"readWindowSec"`
	HeartbeatSec      int               `json:"heartbeatSec"`
	MaxMessages       int               `json:"maxMessages"`
	IncludeCommands   []string          `json:"includeCommands"`
	ExcludeCommands   []string          `json:"excludeCommands"`
	Headers           map[string]string `json:"headers"`
	Buvid3            string            `json:"buvid3"`
}

type httpPollingConsumerConfig struct {
	Method  string                       `json:"method"`
	Headers map[string]string            `json:"headers"`
	Query   map[string]string            `json:"query"`
	Body    map[string]any               `json:"body"`
	Auth    httpPollingAuthConfig        `json:"auth"`
	Paging  httpPollingPagingConfig      `json:"paging"`
	Mapping httpPollingMessageMappingCfg `json:"mapping"`
}

type httpPollingAuthConfig struct {
	Mode   string `json:"mode"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
	Token  string `json:"token"`
}

type httpPollingPagingConfig struct {
	CursorField        string `json:"cursorField"`
	CursorIn           string `json:"cursorIn"`
	CursorMode         string `json:"cursorMode"`
	ResponseCursorPath string `json:"responseCursorPath"`
	ItemCursorPath     string `json:"itemCursorPath"`
	StartCursor        string `json:"startCursor"`
	LimitField         string `json:"limitField"`
	LimitIn            string `json:"limitIn"`
	RoomIDField        string `json:"roomIdField"`
	RoomIDIn           string `json:"roomIdIn"`
	PageStep           int    `json:"pageStep"`
}

type httpPollingMessageMappingCfg struct {
	ItemsPath      string `json:"itemsPath"`
	RoomIDPath     string `json:"roomIdPath"`
	UIDPath        string `json:"uidPath"`
	UnamePath      string `json:"unamePath"`
	ContentPath    string `json:"contentPath"`
	RawPayloadPath string `json:"rawPayloadPath"`
	SourcePath     string `json:"sourcePath"`
}

type bilibiliDanmuInfoEnvelope struct {
	Code    int                   `json:"code"`
	Message string                `json:"message"`
	Msg     string                `json:"msg"`
	Data    bilibiliDanmuInfoData `json:"data"`
}

type bilibiliDanmuInfoData struct {
	Token    string             `json:"token"`
	HostList []bilibiliHostItem `json:"host_list"`
}

type bilibiliHostItem struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	WSSPort int    `json:"wss_port"`
	WSPort  int    `json:"ws_port"`
}

type bilibiliPacket struct {
	Version   uint16
	Operation uint32
	Sequence  uint32
	Payload   []byte
}

func (s *Service) runDanmakuConsumerLoop() {
	defer s.wg.Done()
	stop := s.stopChannel()
	if stop == nil {
		return
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	nextPollAt := time.Time{}
	for {
		select {
		case <-stop:
			s.markConsumerState(func(state *DanmakuConsumerRuntime) {
				state.Running = false
			})
			return
		case <-ticker.C:
			featureEnabled, featureErr := s.IsFeatureEnabled(context.Background(), FeatureDanmakuConsumer)
			if featureErr != nil {
				s.markConsumerState(func(state *DanmakuConsumerRuntime) {
					state.Running = false
					state.LastError = featureErr.Error()
				})
				continue
			}
			if !featureEnabled {
				s.markConsumerState(func(state *DanmakuConsumerRuntime) {
					state.Running = false
				})
				continue
			}
			setting, err := s.store.GetDanmakuConsumerSetting(context.Background())
			if err != nil {
				s.markConsumerState(func(state *DanmakuConsumerRuntime) {
					state.Running = false
					state.LastError = err.Error()
				})
				continue
			}
			if !setting.Enabled || !isDanmakuConsumerConfigReady(setting) {
				s.markConsumerState(func(state *DanmakuConsumerRuntime) {
					state.Running = false
				})
				continue
			}
			s.markConsumerState(func(state *DanmakuConsumerRuntime) {
				state.Running = true
			})
			if !nextPollAt.IsZero() && time.Now().Before(nextPollAt) {
				continue
			}
			_, _ = s.pollDanmakuConsumerOnce(context.Background(), false)
			pollEvery := setting.PollIntervalSec
			if pollEvery <= 0 {
				pollEvery = 3
			}
			nextPollAt = time.Now().Add(time.Duration(pollEvery) * time.Second)
		}
	}
}

func (s *Service) PollDanmakuConsumerOnce(ctx context.Context, force bool) (map[string]any, error) {
	return s.pollDanmakuConsumerOnce(ctx, force)
}

func (s *Service) pollDanmakuConsumerOnce(ctx context.Context, force bool) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureDanmakuConsumer); err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("feature is disabled: danmaku_consumer")
	}
	setting, err := s.store.GetDanmakuConsumerSetting(ctx)
	if err != nil {
		return nil, err
	}
	if !force && !setting.Enabled {
		return nil, errors.New("danmaku consumer is disabled")
	}
	if !isDanmakuConsumerConfigReady(setting) {
		return nil, errors.New("danmaku consumer config is incomplete")
	}
	provider := normalizeDanmakuProvider(setting.Provider)
	if provider == danmakuProviderBilibiliMsgStream {
		result, pollErr := s.pollBilibiliMessageStreamOnce(ctx, setting)
		if pollErr != nil {
			s.recordConsumerFailure(setting.Cursor, pollErr.Error())
		}
		return result, pollErr
	}

	httpCfg, err := parseHTTPPollingConsumerConfig(setting)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSpace(setting.Endpoint)
	if endpoint == "" {
		return nil, errors.New("danmaku consumer endpoint is empty")
	}

	parsedURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	query := parsedURL.Query()
	for key, value := range httpCfg.Query {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		query.Set(key, strings.TrimSpace(value))
	}
	limit := setting.BatchSize
	if limit <= 0 {
		limit = 20
	}
	if limit > 500 {
		limit = 500
	}

	requestBody := copyAnyMap(httpCfg.Body)
	requestCursor := strings.TrimSpace(setting.Cursor)
	if requestCursor == "" {
		requestCursor = strings.TrimSpace(httpCfg.Paging.StartCursor)
	}
	if requestCursor != "" {
		applyHTTPPollingRequestField(query, requestBody, httpCfg.Paging.CursorIn, httpCfg.Paging.CursorField, requestCursor)
	}
	applyHTTPPollingRequestField(query, requestBody, httpCfg.Paging.LimitIn, httpCfg.Paging.LimitField, strconv.Itoa(limit))
	if setting.RoomID > 0 {
		applyHTTPPollingRequestField(query, requestBody, httpCfg.Paging.RoomIDIn, httpCfg.Paging.RoomIDField, strconv.FormatInt(setting.RoomID, 10))
	}

	token := strings.TrimSpace(httpCfg.Auth.Token)
	if token == "" {
		token = strings.TrimSpace(setting.AuthToken)
	}
	extraHeaders := map[string]string{}
	if authErr := applyHTTPPollingAuth(httpCfg.Auth, token, query, requestBody, extraHeaders); authErr != nil {
		return nil, authErr
	}
	parsedURL.RawQuery = query.Encode()

	method := normalizeHTTPMethod(httpCfg.Method)
	var requestBodyReader io.Reader
	if method == http.MethodPost {
		encodedBody, encodeErr := json.Marshal(requestBody)
		if encodeErr != nil {
			return nil, encodeErr
		}
		requestBodyReader = bytes.NewReader(encodedBody)
	} else if len(requestBody) > 0 {
		return nil, errors.New("http_polling method=GET cannot send request body, set method=POST or move fields to query")
	}

	req, err := http.NewRequestWithContext(ctx, method, parsedURL.String(), requestBodyReader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gover-danmaku-consumer/1.0")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range httpCfg.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	for key, value := range extraHeaders {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := danmakuConsumerHTTPClient.Do(req)
	if err != nil {
		s.recordConsumerFailure(setting.Cursor, err.Error())
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		s.recordConsumerFailure(setting.Cursor, err.Error())
		return nil, err
	}
	bodyText := strings.TrimSpace(string(bodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		failMessage := fmt.Sprintf("consumer endpoint status=%d body=%s", resp.StatusCode, truncateText(bodyText, 400))
		s.recordConsumerFailure(setting.Cursor, failMessage)
		return nil, errors.New(failMessage)
	}

	items, nextCursor, parseErr := parseDanmakuConsumerResponse(bodyBytes, httpCfg)
	if parseErr != nil {
		s.recordConsumerFailure(setting.Cursor, parseErr.Error())
		return nil, parseErr
	}
	processed := 0
	matched := 0
	failed := make([]map[string]any, 0)
	lastCursor := strings.TrimSpace(nextCursor)

	for idx, item := range items {
		// Skip invalid message lines to keep polling pipeline robust.
		if item.RoomID <= 0 || strings.TrimSpace(item.Content) == "" {
			continue
		}
		if strings.TrimSpace(item.Source) == "" {
			item.Source = "consumer." + strings.TrimSpace(setting.Provider)
		}
		dispatchResult, dispatchErr := s.DispatchDanmaku(ctx, item)
		if dispatchErr != nil {
			failed = append(failed, map[string]any{
				"index":   idx,
				"roomId":  item.RoomID,
				"content": item.Content,
				"error":   dispatchErr.Error(),
			})
			continue
		}
		processed++
		matched += dispatchResult.MatchedCount
		if strings.TrimSpace(lastCursor) == "" {
			lastCursor = deriveCursorFromDanmaku(item, idx, requestCursor, httpCfg.Paging)
		}
	}
	if strings.TrimSpace(lastCursor) == "" {
		lastCursor = deriveCursorFromPaging(requestCursor, len(items), limit, httpCfg.Paging)
	}
	if strings.TrimSpace(lastCursor) == "" {
		lastCursor = strings.TrimSpace(setting.Cursor)
	}

	now := time.Now().UTC()
	lastErr := ""
	if len(failed) > 0 {
		lastErr = fmt.Sprintf("processed=%d failed=%d", processed, len(failed))
	}
	_ = s.store.UpdateDanmakuConsumerRuntime(ctx, lastCursor, lastErr, now)
	s.markConsumerState(func(state *DanmakuConsumerRuntime) {
		state.Running = setting.Enabled
		state.LastCursor = lastCursor
		state.LastError = lastErr
		state.LastFetched = len(items)
		state.LastProcessed = processed
		state.LastMatched = matched
		t := now
		state.LastPollAt = &t
	})

	_ = s.SaveLiveEventJSON(ctx, "danmaku.consumer.poll", map[string]any{
		"provider":      setting.Provider,
		"endpoint":      setting.Endpoint,
		"fetched":       len(items),
		"processed":     processed,
		"matchedCount":  matched,
		"failedCount":   len(failed),
		"cursor":        lastCursor,
		"roomIdFilter":  setting.RoomID,
		"pollIntervalS": setting.PollIntervalSec,
	})

	return map[string]any{
		"provider":     setting.Provider,
		"endpoint":     setting.Endpoint,
		"fetched":      len(items),
		"processed":    processed,
		"matchedCount": matched,
		"failed":       failed,
		"cursor":       lastCursor,
		"time":         now.Format(time.RFC3339),
	}, nil
}

func (s *Service) recordConsumerFailure(cursor string, detail string) {
	_ = s.store.UpdateDanmakuConsumerRuntime(context.Background(), cursor, detail, time.Now().UTC())
	s.markConsumerState(func(state *DanmakuConsumerRuntime) {
		state.LastError = detail
	})
	_ = s.SaveLiveEventJSON(context.Background(), "danmaku.consumer.error", map[string]any{
		"cursor": cursor,
		"error":  detail,
	})
}

func parseHTTPPollingConsumerConfig(setting *store.DanmakuConsumerSetting) (httpPollingConsumerConfig, error) {
	cfg := httpPollingConsumerConfig{
		Method:  http.MethodGet,
		Headers: map[string]string{},
		Query:   map[string]string{},
		Body:    map[string]any{},
		Auth: httpPollingAuthConfig{
			Mode:   "bearer",
			Prefix: "Bearer ",
		},
		Paging: httpPollingPagingConfig{
			CursorField:        "cursor",
			CursorIn:           "query",
			CursorMode:         "cursor",
			ResponseCursorPath: "nextCursor|cursor|next|offset|data.nextCursor|data.cursor|data.next|data.offset",
			ItemCursorPath:     "cursor|id|offset|time",
			LimitField:         "limit",
			LimitIn:            "query",
			RoomIDField:        "roomId",
			RoomIDIn:           "query",
			PageStep:           1,
		},
		Mapping: httpPollingMessageMappingCfg{},
	}
	if setting == nil {
		return cfg, errors.New("empty danmaku consumer setting")
	}
	if strings.TrimSpace(setting.ConfigJSON) != "" {
		if err := json.Unmarshal([]byte(setting.ConfigJSON), &cfg); err != nil {
			return cfg, errors.New("invalid consumer configJson: " + err.Error())
		}
	}
	cfg.Method = normalizeHTTPMethod(cfg.Method)
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if cfg.Query == nil {
		cfg.Query = map[string]string{}
	}
	if cfg.Body == nil {
		cfg.Body = map[string]any{}
	}
	cfg.Auth.Mode = normalizeHTTPPollingAuthMode(cfg.Auth.Mode)
	if strings.TrimSpace(cfg.Auth.Prefix) == "" {
		cfg.Auth.Prefix = "Bearer "
	}
	cfg.Paging.CursorField = defaultString(strings.TrimSpace(cfg.Paging.CursorField), "cursor")
	cfg.Paging.CursorIn = normalizeFieldLocation(cfg.Paging.CursorIn, "query")
	cfg.Paging.CursorMode = normalizeCursorMode(cfg.Paging.CursorMode)
	cfg.Paging.ResponseCursorPath = strings.TrimSpace(cfg.Paging.ResponseCursorPath)
	if cfg.Paging.ResponseCursorPath == "" {
		cfg.Paging.ResponseCursorPath = "nextCursor|cursor|next|offset|data.nextCursor|data.cursor|data.next|data.offset"
	}
	cfg.Paging.ItemCursorPath = strings.TrimSpace(cfg.Paging.ItemCursorPath)
	if cfg.Paging.ItemCursorPath == "" {
		cfg.Paging.ItemCursorPath = "cursor|id|offset|time"
	}
	cfg.Paging.LimitField = defaultString(strings.TrimSpace(cfg.Paging.LimitField), "limit")
	cfg.Paging.LimitIn = normalizeFieldLocation(cfg.Paging.LimitIn, "query")
	cfg.Paging.RoomIDField = defaultString(strings.TrimSpace(cfg.Paging.RoomIDField), "roomId")
	cfg.Paging.RoomIDIn = normalizeFieldLocation(cfg.Paging.RoomIDIn, "query")
	if cfg.Paging.PageStep <= 0 {
		cfg.Paging.PageStep = 1
	}
	return cfg, nil
}

func normalizeHTTPMethod(method string) string {
	method = strings.ToUpper(strings.TrimSpace(method))
	switch method {
	case "", http.MethodGet:
		return http.MethodGet
	case http.MethodPost:
		return http.MethodPost
	default:
		return http.MethodGet
	}
}

func normalizeHTTPPollingAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "none", "disabled", "off":
		return "none"
	case "header":
		return "header"
	case "query":
		return "query"
	case "body":
		return "body"
	case "", "bearer", "authorization":
		return "bearer"
	default:
		return "bearer"
	}
}

func normalizeFieldLocation(value string, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "body":
		return "body"
	case "query":
		return "query"
	default:
		return fallback
	}
}

func normalizeCursorMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "offset":
		return "offset"
	case "page":
		return "page"
	default:
		return "cursor"
	}
}

func copyAnyMap(src map[string]any) map[string]any {
	dst := map[string]any{}
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func applyHTTPPollingRequestField(query url.Values, body map[string]any, in string, field string, value string) {
	field = strings.TrimSpace(field)
	if field == "" || strings.TrimSpace(value) == "" {
		return
	}
	if normalizeFieldLocation(in, "query") == "body" {
		setMapPath(body, field, value)
		return
	}
	query.Set(field, value)
}

func applyHTTPPollingAuth(cfg httpPollingAuthConfig, token string, query url.Values, body map[string]any, headers map[string]string) error {
	mode := normalizeHTTPPollingAuthMode(cfg.Mode)
	token = strings.TrimSpace(token)
	if mode == "none" || token == "" {
		return nil
	}
	switch mode {
	case "bearer":
		prefix := cfg.Prefix
		if strings.TrimSpace(prefix) == "" {
			prefix = "Bearer "
		}
		headers["Authorization"] = prefix + token
		return nil
	case "header":
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			name = "X-Auth-Token"
		}
		headers[name] = token
		return nil
	case "query":
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			name = "token"
		}
		query.Set(name, token)
		return nil
	case "body":
		name := strings.TrimSpace(cfg.Name)
		if name == "" {
			name = "token"
		}
		setMapPath(body, name, token)
		return nil
	default:
		return errors.New("unsupported consumer auth mode: " + mode)
	}
}

func parseDanmakuConsumerResponse(body []byte, cfg httpPollingConsumerConfig) ([]DanmakuDispatchRequest, string, error) {
	raw := map[string]any{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, "", err
	}
	itemsAny := pickByPathCandidates(raw, cfg.Mapping.ItemsPath, "items", "messages", "records", "list", "data.items", "data.messages", "data.records", "data.list")
	nextCursor := anyToString(pickByPathCandidates(raw, cfg.Paging.ResponseCursorPath))
	itemsList := coerceAnySlice(itemsAny)
	if len(itemsList) == 0 {
		return []DanmakuDispatchRequest{}, nextCursor, nil
	}

	result := make([]DanmakuDispatchRequest, 0, len(itemsList))
	for _, line := range itemsList {
		obj, ok := line.(map[string]any)
		if !ok {
			continue
		}
		roomID := anyToInt64(pickByPathCandidates(obj, cfg.Mapping.RoomIDPath, "roomId", "room_id", "room"))
		uid := anyToInt64(pickByPathCandidates(obj, cfg.Mapping.UIDPath, "uid", "userId", "user_id"))
		uname := anyToString(pickByPathCandidates(obj, cfg.Mapping.UnamePath, "uname", "userName", "username", "name"))
		content := strings.TrimSpace(anyToString(pickByPathCandidates(obj, cfg.Mapping.ContentPath, "content", "msg", "message", "text")))
		rawPayload := anyToString(pickByPathCandidates(obj, cfg.Mapping.RawPayloadPath, "rawPayload", "raw_payload", "raw"))
		if strings.TrimSpace(rawPayload) == "" {
			if encoded, err := json.Marshal(obj); err == nil {
				rawPayload = string(encoded)
			}
		}
		source := anyToString(pickByPathCandidates(obj, cfg.Mapping.SourcePath, "source", "sourceType"))
		item := DanmakuDispatchRequest{
			RoomID:     roomID,
			UID:        uid,
			Uname:      uname,
			Content:    content,
			RawPayload: rawPayload,
			Source:     source,
		}
		if strings.TrimSpace(nextCursor) == "" {
			nextCursor = anyToString(pickByPathCandidates(obj, cfg.Paging.ItemCursorPath, "cursor", "id", "offset", "time"))
		}
		result = append(result, item)
	}
	return result, strings.TrimSpace(nextCursor), nil
}

func deriveCursorFromDanmaku(item DanmakuDispatchRequest, index int, current string, paging httpPollingPagingConfig) string {
	if normalizeCursorMode(paging.CursorMode) != "cursor" {
		if derived := deriveCursorFromPaging(current, index+1, index+1, paging); strings.TrimSpace(derived) != "" {
			return derived
		}
	}
	if strings.TrimSpace(item.RawPayload) != "" {
		return strconv.Itoa(index + 1)
	}
	if normalizeCursorMode(paging.CursorMode) == "cursor" && strings.TrimSpace(current) != "" {
		return strings.TrimSpace(current)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func deriveCursorFromPaging(current string, fetched int, limit int, paging httpPollingPagingConfig) string {
	current = strings.TrimSpace(current)
	mode := normalizeCursorMode(paging.CursorMode)
	switch mode {
	case "offset":
		base := int64(0)
		if current != "" {
			if parsed, err := strconv.ParseInt(current, 10, 64); err == nil {
				base = parsed
			}
		}
		step := fetched
		if step <= 0 {
			step = limit
		}
		if step <= 0 {
			step = 1
		}
		return strconv.FormatInt(base+int64(step), 10)
	case "page":
		base := int64(0)
		if current != "" {
			if parsed, err := strconv.ParseInt(current, 10, 64); err == nil {
				base = parsed
			}
		}
		step := paging.PageStep
		if step <= 0 {
			step = 1
		}
		return strconv.FormatInt(base+int64(step), 10)
	default:
		return current
	}
}

func pickField(payload map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := payload[key]; ok {
			return value
		}
	}
	return nil
}

func pickByPathCandidates(root any, custom string, defaults ...string) any {
	candidates := append(splitPathCandidates(custom), defaults...)
	for _, candidate := range candidates {
		if value, ok := lookupPath(root, candidate); ok {
			return value
		}
	}
	return nil
}

func splitPathCandidates(raw string) []string {
	raw = strings.ReplaceAll(raw, "\n", "|")
	raw = strings.ReplaceAll(raw, ",", "|")
	parts := strings.Split(raw, "|")
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

func lookupPath(root any, path string) (any, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, false
	}
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return root, true
	}
	current := root
	segments := strings.Split(path, ".")
	for _, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return nil, false
		}
		next, ok := lookupPathSegment(current, segment)
		if !ok {
			return nil, false
		}
		current = next
	}
	return current, true
}

func lookupPathSegment(current any, segment string) (any, bool) {
	for len(segment) > 0 {
		if strings.HasPrefix(segment, "[") {
			end := strings.Index(segment, "]")
			if end <= 1 {
				return nil, false
			}
			indexText := strings.TrimSpace(segment[1:end])
			index, err := strconv.Atoi(indexText)
			if err != nil || index < 0 {
				return nil, false
			}
			items := coerceAnySlice(current)
			if index >= len(items) {
				return nil, false
			}
			current = items[index]
			segment = strings.TrimSpace(segment[end+1:])
			continue
		}
		name := segment
		if idx := strings.Index(segment, "["); idx >= 0 {
			name = strings.TrimSpace(segment[:idx])
			segment = strings.TrimSpace(segment[idx:])
		} else {
			segment = ""
		}
		if name == "" {
			continue
		}
		obj, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		value, exists := obj[name]
		if !exists {
			return nil, false
		}
		current = value
	}
	return current, true
}

func coerceAnySlice(value any) []any {
	if value == nil {
		return []any{}
	}
	switch v := value.(type) {
	case []any:
		return v
	case []map[string]any:
		items := make([]any, 0, len(v))
		for _, item := range v {
			items = append(items, item)
		}
		return items
	default:
		return []any{}
	}
}

func setMapPath(target map[string]any, path string, value any) {
	if target == nil {
		return
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return
	}
	path = strings.TrimPrefix(path, "$.")
	path = strings.TrimPrefix(path, ".")
	parts := strings.Split(path, ".")
	current := target
	for idx, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if idx == len(parts)-1 {
			current[part] = value
			return
		}
		next, ok := current[part]
		if !ok {
			newMap := map[string]any{}
			current[part] = newMap
			current = newMap
			continue
		}
		nextMap, ok := next.(map[string]any)
		if !ok {
			newMap := map[string]any{}
			current[part] = newMap
			current = newMap
			continue
		}
		current = nextMap
	}
}

func anyToString(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case json.Number:
		return strings.TrimSpace(v.String())
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return ""
	}
}

func anyToInt64(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		i, _ := v.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return i
	default:
		return 0
	}
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit] + "...(truncated)"
}

var (
	wbiKeyCacheMu      = make(chan struct{}, 1)
	cachedWBIImgKey    string
	cachedWBISubKey    string
	cachedWBIExpiresAt time.Time
)

func init() {
	// Light-weight mutex without pulling extra sync primitives for this file.
	wbiKeyCacheMu <- struct{}{}
}

func normalizeDanmakuProvider(provider string) string {
	value := strings.ToLower(strings.TrimSpace(provider))
	switch value {
	case "", danmakuProviderHTTPPolling:
		return danmakuProviderHTTPPolling
	case "bilibili_message_stream", "bilibili_live_ws", "bilibili_ws", "bilibili_message_ws", "live_message_stream":
		return danmakuProviderBilibiliMsgStream
	default:
		return value
	}
}

func isDanmakuConsumerConfigReady(setting *store.DanmakuConsumerSetting) bool {
	if setting == nil {
		return false
	}
	provider := normalizeDanmakuProvider(setting.Provider)
	if provider == danmakuProviderBilibiliMsgStream {
		if setting.RoomID > 0 {
			return true
		}
		cfg := bilibiliMessageStreamConfig{}
		if strings.TrimSpace(setting.ConfigJSON) != "" {
			if err := json.Unmarshal([]byte(setting.ConfigJSON), &cfg); err != nil {
				return false
			}
		}
		return cfg.RoomID > 0
	}
	return strings.TrimSpace(setting.Endpoint) != ""
}

func (s *Service) pollBilibiliMessageStreamOnce(ctx context.Context, setting *store.DanmakuConsumerSetting) (map[string]any, error) {
	cfg, err := parseBilibiliMessageStreamConfig(setting)
	if err != nil {
		return nil, err
	}
	roomID := setting.RoomID
	if roomID <= 0 {
		roomID = cfg.RoomID
	}
	if roomID <= 0 {
		return nil, errors.New("roomId is required for bilibili_message_stream")
	}

	cookieHeader := ""
	if cfg.UseCookie {
		if cookieSetting, cookieErr := s.store.GetCookieSetting(ctx); cookieErr == nil {
			cookieHeader = strings.TrimSpace(cookieSetting.Content)
		}
	}
	if strings.TrimSpace(cfg.Buvid3) == "" {
		cfg.Buvid3 = parseCookieValue(cookieHeader, "buvid3")
	}

	info, tokenURL, err := s.fetchBilibiliDanmuInfo(ctx, cfg, roomID, cookieHeader)
	if err != nil {
		return nil, err
	}
	wsURL, err := buildBilibiliWSURL(cfg, info.Data.HostList)
	if err != nil {
		return nil, err
	}

	headers := make(http.Header)
	headers.Set("User-Agent", "gover-danmaku-consumer/1.0")
	headers.Set("Origin", "https://live.bilibili.com")
	headers.Set("Referer", "https://live.bilibili.com/"+strconv.FormatInt(roomID, 10))
	if cfg.UseCookie {
		headers.Set("Cookie", mergeCookiePair(cookieHeader, "buvid3", cfg.Buvid3))
	}
	for key, value := range cfg.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		headers.Set(key, value)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: time.Duration(cfg.ConnectTimeoutSec) * time.Second,
	}
	conn, resp, err := dialer.DialContext(ctx, wsURL, headers)
	if err != nil {
		msg := "connect bilibili ws failed: " + err.Error()
		if resp != nil {
			msg = fmt.Sprintf("%s (http=%d)", msg, resp.StatusCode)
		}
		return nil, errors.New(msg)
	}
	defer conn.Close()

	authBody, err := json.Marshal(map[string]any{
		"uid":      cfg.UID,
		"roomid":   roomID,
		"protover": cfg.Protover,
		"platform": cfg.Platform,
		"type":     cfg.Type,
		"key":      strings.TrimSpace(info.Data.Token),
	})
	if err != nil {
		return nil, err
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, buildBilibiliPacket(7, 1, 1, authBody)); err != nil {
		return nil, err
	}
	_ = conn.WriteMessage(websocket.BinaryMessage, buildBilibiliPacket(2, 1, 1, []byte("[object Object]")))

	readWindow := time.Duration(cfg.ReadWindowSec) * time.Second
	deadline := time.Now().Add(readWindow)
	heartbeatTicker := time.NewTicker(time.Duration(cfg.HeartbeatSec) * time.Second)
	defer heartbeatTicker.Stop()

	commandCounter := map[string]int{}
	processed := 0
	matched := 0
	fetched := 0
	lastCursor := strings.TrimSpace(setting.Cursor)
	failed := make([]map[string]any, 0)
	authCode := int64(-1)

loop:
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			break loop
		case <-heartbeatTicker.C:
			_ = conn.WriteMessage(websocket.BinaryMessage, buildBilibiliPacket(2, 1, 1, []byte("[object Object]")))
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, frame, readErr := conn.ReadMessage()
		if readErr != nil {
			if netErr, ok := readErr.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				break
			}
			failed = append(failed, map[string]any{"error": readErr.Error()})
			break
		}

		packets, decodeErr := decodeBilibiliPackets(frame)
		if decodeErr != nil {
			failed = append(failed, map[string]any{"error": "decode packet failed: " + decodeErr.Error()})
			continue
		}
		for _, packet := range packets {
			switch packet.Operation {
			case 8:
				authCode = int64(parseBilibiliAuthCode(packet.Payload))
			case 5:
				payload := bytes.TrimSpace(packet.Payload)
				if len(payload) == 0 {
					continue
				}
				event := map[string]any{}
				if err := json.Unmarshal(payload, &event); err != nil {
					continue
				}
				cmd := normalizeBilibiliCommand(anyToString(event["cmd"]))
				if cmd == "" {
					cmd = "UNKNOWN"
				}
				commandCounter[cmd]++
				if !allowBilibiliCommand(cfg, cmd) {
					continue
				}
				item, ok := parseBilibiliDanmakuPayload(event, roomID, strings.TrimSpace(setting.Provider))
				if !ok {
					continue
				}
				fetched++
				dispatchResult, dispatchErr := s.DispatchDanmaku(ctx, item)
				if dispatchErr != nil {
					failed = append(failed, map[string]any{
						"roomId":  item.RoomID,
						"content": item.Content,
						"error":   dispatchErr.Error(),
					})
					continue
				}
				processed++
				matched += dispatchResult.MatchedCount
				lastCursor = deriveCursorFromBilibiliPayload(event, fetched, lastCursor)
				if cfg.MaxMessages > 0 && fetched >= cfg.MaxMessages {
					break loop
				}
			}
		}
	}

	now := time.Now().UTC()
	lastErr := ""
	if authCode > 0 {
		lastErr = "authCode=" + strconv.FormatInt(authCode, 10)
	}
	if len(failed) > 0 {
		lastErr = fmt.Sprintf("processed=%d failed=%d", processed, len(failed))
	}
	_ = s.store.UpdateDanmakuConsumerRuntime(ctx, lastCursor, lastErr, now)
	s.markConsumerState(func(state *DanmakuConsumerRuntime) {
		state.Running = setting.Enabled
		state.LastCursor = lastCursor
		state.LastError = lastErr
		state.LastFetched = fetched
		state.LastProcessed = processed
		state.LastMatched = matched
		t := now
		state.LastPollAt = &t
	})

	_ = s.SaveLiveEventJSON(ctx, "danmaku.consumer.poll", map[string]any{
		"provider":      normalizeDanmakuProvider(setting.Provider),
		"tokenEndpoint": tokenURL,
		"wsURL":         wsURL,
		"fetched":       fetched,
		"processed":     processed,
		"matchedCount":  matched,
		"failedCount":   len(failed),
		"cursor":        lastCursor,
		"roomIdFilter":  roomID,
		"authCode":      authCode,
		"commandTop":    topCommandSummary(commandCounter, 8),
	})

	return map[string]any{
		"provider":      normalizeDanmakuProvider(setting.Provider),
		"tokenEndpoint": tokenURL,
		"wsURL":         wsURL,
		"fetched":       fetched,
		"processed":     processed,
		"matchedCount":  matched,
		"failed":        failed,
		"cursor":        lastCursor,
		"authCode":      authCode,
		"commandTop":    topCommandSummary(commandCounter, 8),
		"time":          now.Format(time.RFC3339),
	}, nil
}

func parseBilibiliMessageStreamConfig(setting *store.DanmakuConsumerSetting) (bilibiliMessageStreamConfig, error) {
	if setting == nil {
		return bilibiliMessageStreamConfig{}, errors.New("empty danmaku consumer setting")
	}
	cfg := bilibiliMessageStreamConfig{
		RoomID:            setting.RoomID,
		TokenEndpoint:     strings.TrimSpace(setting.Endpoint),
		UseWBI:            true,
		UseCookie:         true,
		UID:               0,
		Protover:          3,
		Platform:          "web",
		Type:              2,
		WebLocation:       "444.8",
		WSPath:            "/sub",
		PreferWSS:         true,
		ConnectTimeoutSec: 10,
		ReadWindowSec:     8,
		HeartbeatSec:      30,
		MaxMessages:       clampRange(setting.BatchSize, 20, 1000, 50),
		Headers:           map[string]string{},
	}
	if setting.PollIntervalSec > 0 {
		cfg.ReadWindowSec = clampRange(setting.PollIntervalSec*2, 4, 120, 8)
	}
	if strings.TrimSpace(setting.ConfigJSON) != "" {
		if err := json.Unmarshal([]byte(setting.ConfigJSON), &cfg); err != nil {
			return cfg, errors.New("invalid consumer configJson: " + err.Error())
		}
	}
	if strings.TrimSpace(cfg.TokenEndpoint) == "" {
		cfg.TokenEndpoint = defaultDanmuInfoEndpoint
	}
	if cfg.Protover <= 0 {
		cfg.Protover = 3
	}
	if cfg.Type <= 0 {
		cfg.Type = 2
	}
	if strings.TrimSpace(cfg.Platform) == "" {
		cfg.Platform = "web"
	}
	if strings.TrimSpace(cfg.WSPath) == "" {
		cfg.WSPath = "/sub"
	}
	if cfg.ConnectTimeoutSec <= 0 {
		cfg.ConnectTimeoutSec = 10
	}
	if cfg.ConnectTimeoutSec > 60 {
		cfg.ConnectTimeoutSec = 60
	}
	cfg.ReadWindowSec = clampRange(cfg.ReadWindowSec, 2, 300, 8)
	cfg.HeartbeatSec = clampRange(cfg.HeartbeatSec, 10, 60, 30)
	cfg.MaxMessages = clampRange(cfg.MaxMessages, 1, 2000, 50)
	return cfg, nil
}

func (s *Service) fetchBilibiliDanmuInfo(ctx context.Context, cfg bilibiliMessageStreamConfig, roomID int64, cookieHeader string) (*bilibiliDanmuInfoEnvelope, string, error) {
	tokenEndpoint := strings.TrimSpace(cfg.TokenEndpoint)
	if tokenEndpoint == "" {
		tokenEndpoint = defaultDanmuInfoEndpoint
	}
	parsedURL, err := url.Parse(tokenEndpoint)
	if err != nil {
		return nil, "", err
	}
	query := parsedURL.Query()
	if query.Get("id") == "" {
		query.Set("id", strconv.FormatInt(roomID, 10))
	}
	if query.Get("type") == "" {
		query.Set("type", "0")
	}
	if strings.TrimSpace(cfg.WebLocation) != "" && query.Get("web_location") == "" {
		query.Set("web_location", strings.TrimSpace(cfg.WebLocation))
	}
	if cfg.UseWBI {
		if signErr := s.signWBIQuery(ctx, query, cookieHeader, cfg); signErr != nil {
			return nil, "", signErr
		}
	}
	parsedURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gover-danmaku-consumer/1.0")
	req.Header.Set("Referer", "https://live.bilibili.com/"+strconv.FormatInt(roomID, 10))
	req.Header.Set("Origin", "https://live.bilibili.com")
	if cfg.UseCookie {
		req.Header.Set("Cookie", mergeCookiePair(cookieHeader, "buvid3", cfg.Buvid3))
	}
	for key, value := range cfg.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		req.Header.Set(key, value)
	}

	resp, err := danmakuConsumerHTTPClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	bodyText := strings.TrimSpace(string(bodyBytes))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", errors.New(fmt.Sprintf("getDanmuInfo status=%d body=%s", resp.StatusCode, truncateText(bodyText, 300)))
	}

	info := bilibiliDanmuInfoEnvelope{}
	if err := json.Unmarshal(bodyBytes, &info); err != nil {
		return nil, "", err
	}
	if info.Code != 0 {
		return nil, "", errors.New(fmt.Sprintf("getDanmuInfo code=%d message=%s", info.Code, defaultString(info.Message, info.Msg)))
	}
	if strings.TrimSpace(info.Data.Token) == "" {
		return nil, "", errors.New("getDanmuInfo token is empty")
	}
	return &info, parsedURL.String(), nil
}

func (s *Service) signWBIQuery(ctx context.Context, query url.Values, cookieHeader string, cfg bilibiliMessageStreamConfig) error {
	imgKey, subKey, err := s.loadWBIKeys(ctx, cookieHeader, cfg)
	if err != nil {
		return err
	}
	mixin := generateWBIMixinKey(imgKey, subKey)
	if mixin == "" {
		return errors.New("wbi mixin key is empty")
	}
	query.Del("w_rid")
	query.Set("wts", strconv.FormatInt(time.Now().Unix(), 10))

	keys := make([]string, 0, len(query))
	for key := range query {
		if key == "w_rid" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		value := sanitizeWBIValue(query.Get(key))
		parts = append(parts, encodeURIComponent(key)+"="+encodeURIComponent(value))
	}
	rawQuery := strings.Join(parts, "&")
	sum := md5.Sum([]byte(rawQuery + mixin))
	query.Set("w_rid", hex.EncodeToString(sum[:]))
	return nil
}

func (s *Service) loadWBIKeys(ctx context.Context, cookieHeader string, cfg bilibiliMessageStreamConfig) (string, string, error) {
	<-wbiKeyCacheMu
	defer func() { wbiKeyCacheMu <- struct{}{} }()

	if strings.TrimSpace(cachedWBIImgKey) != "" && strings.TrimSpace(cachedWBISubKey) != "" && time.Now().Before(cachedWBIExpiresAt) {
		return cachedWBIImgKey, cachedWBISubKey, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, defaultBilibiliNavEndpoint, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gover-danmaku-consumer/1.0")
	req.Header.Set("Referer", "https://www.bilibili.com/")
	if cfg.UseCookie {
		req.Header.Set("Cookie", mergeCookiePair(cookieHeader, "buvid3", cfg.Buvid3))
	}

	resp, err := danmakuConsumerHTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", errors.New(fmt.Sprintf("nav status=%d body=%s", resp.StatusCode, truncateText(string(bodyBytes), 300)))
	}

	var payload struct {
		Data struct {
			WBIImg struct {
				ImgURL string `json:"img_url"`
				SubURL string `json:"sub_url"`
			} `json:"wbi_img"`
		} `json:"data"`
	}
	if err := json.Unmarshal(bodyBytes, &payload); err != nil {
		return "", "", err
	}
	imgKey := extractWBIKey(payload.Data.WBIImg.ImgURL)
	subKey := extractWBIKey(payload.Data.WBIImg.SubURL)
	if imgKey == "" || subKey == "" {
		return "", "", errors.New("nav response missing wbi keys")
	}

	cachedWBIImgKey = imgKey
	cachedWBISubKey = subKey
	cachedWBIExpiresAt = time.Now().Add(6 * time.Hour)
	return imgKey, subKey, nil
}

func generateWBIMixinKey(imgKey string, subKey string) string {
	raw := []rune(imgKey + subKey)
	if len(raw) < 64 {
		return ""
	}
	builder := strings.Builder{}
	builder.Grow(64)
	for _, idx := range mixinKeyEncTable {
		if idx < 0 || idx >= len(raw) {
			continue
		}
		builder.WriteRune(raw[idx])
	}
	result := builder.String()
	if len(result) > 32 {
		return result[:32]
	}
	return result
}

func extractWBIKey(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	name := path.Base(parsed.Path)
	if name == "." || name == "/" || name == "" {
		return ""
	}
	if idx := strings.Index(name, "."); idx > 0 {
		return name[:idx]
	}
	return name
}

func sanitizeWBIValue(value string) string {
	filtered := strings.Map(func(r rune) rune {
		switch r {
		case '!', '\'', '(', ')', '*':
			return -1
		default:
			return r
		}
	}, value)
	return filtered
}

func encodeURIComponent(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func buildBilibiliWSURL(cfg bilibiliMessageStreamConfig, hostList []bilibiliHostItem) (string, error) {
	selectedHost := strings.TrimSpace(cfg.WSHost)
	selectedPort := cfg.WSPort
	if selectedHost == "" {
		for _, host := range hostList {
			if strings.TrimSpace(host.Host) == "" {
				continue
			}
			selectedHost = strings.TrimSpace(host.Host)
			if cfg.PreferWSS {
				selectedPort = host.WSSPort
				if selectedPort <= 0 {
					selectedPort = host.Port
				}
			} else {
				selectedPort = host.WSPort
				if selectedPort <= 0 {
					selectedPort = host.Port
				}
			}
			break
		}
	}
	if selectedHost == "" {
		return "", errors.New("empty danmaku ws host")
	}
	if selectedPort <= 0 {
		if cfg.PreferWSS {
			selectedPort = 443
		} else {
			selectedPort = 80
		}
	}
	scheme := "wss"
	if !cfg.PreferWSS {
		scheme = "ws"
	}
	wsPath := strings.TrimSpace(cfg.WSPath)
	if wsPath == "" {
		wsPath = "/sub"
	}
	if !strings.HasPrefix(wsPath, "/") {
		wsPath = "/" + wsPath
	}
	return (&url.URL{
		Scheme: scheme,
		Host:   fmt.Sprintf("%s:%d", selectedHost, selectedPort),
		Path:   wsPath,
	}).String(), nil
}

func buildBilibiliPacket(operation uint32, version uint16, sequence uint32, payload []byte) []byte {
	packetSize := 16 + len(payload)
	buf := make([]byte, packetSize)
	binary.BigEndian.PutUint32(buf[0:4], uint32(packetSize))
	binary.BigEndian.PutUint16(buf[4:6], 16)
	binary.BigEndian.PutUint16(buf[6:8], version)
	binary.BigEndian.PutUint32(buf[8:12], operation)
	binary.BigEndian.PutUint32(buf[12:16], sequence)
	copy(buf[16:], payload)
	return buf
}

func decodeBilibiliPackets(frame []byte) ([]bilibiliPacket, error) {
	offset := 0
	result := make([]bilibiliPacket, 0, 16)
	for offset+16 <= len(frame) {
		packetLen := int(binary.BigEndian.Uint32(frame[offset : offset+4]))
		if packetLen < 16 || offset+packetLen > len(frame) {
			return result, errors.New("invalid packet length")
		}
		headerLen := int(binary.BigEndian.Uint16(frame[offset+4 : offset+6]))
		if headerLen < 16 || headerLen > packetLen {
			return result, errors.New("invalid packet header length")
		}
		version := binary.BigEndian.Uint16(frame[offset+6 : offset+8])
		operation := binary.BigEndian.Uint32(frame[offset+8 : offset+12])
		sequence := binary.BigEndian.Uint32(frame[offset+12 : offset+16])
		payload := frame[offset+headerLen : offset+packetLen]
		packet := bilibiliPacket{
			Version:   version,
			Operation: operation,
			Sequence:  sequence,
			Payload:   append([]byte(nil), payload...),
		}
		switch version {
		case 2:
			decoded, err := decodeZlibPayload(payload)
			if err == nil && len(decoded) > 0 {
				nested, nestedErr := decodeBilibiliPackets(decoded)
				if nestedErr == nil && len(nested) > 0 {
					result = append(result, nested...)
				}
			}
		case 3:
			decoded, err := decodeBrotliPayload(payload)
			if err == nil && len(decoded) > 0 {
				nested, nestedErr := decodeBilibiliPackets(decoded)
				if nestedErr == nil && len(nested) > 0 {
					result = append(result, nested...)
				}
			}
		default:
			result = append(result, packet)
		}
		offset += packetLen
	}
	return result, nil
}

func decodeZlibPayload(payload []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, 8<<20))
}

func decodeBrotliPayload(payload []byte) ([]byte, error) {
	reader := brotli.NewReader(bytes.NewReader(payload))
	return io.ReadAll(io.LimitReader(reader, 8<<20))
}

func parseBilibiliAuthCode(payload []byte) int {
	value := map[string]any{}
	if err := json.Unmarshal(payload, &value); err != nil {
		return -1
	}
	return int(anyToInt64(value["code"]))
}

func normalizeBilibiliCommand(command string) string {
	command = strings.ToUpper(strings.TrimSpace(command))
	if command == "" {
		return ""
	}
	if idx := strings.Index(command, ":"); idx > 0 {
		command = command[:idx]
	}
	return command
}

func allowBilibiliCommand(cfg bilibiliMessageStreamConfig, cmd string) bool {
	cmd = normalizeBilibiliCommand(cmd)
	if cmd == "" {
		return false
	}
	if len(cfg.IncludeCommands) > 0 {
		allowed := false
		for _, item := range cfg.IncludeCommands {
			if normalizeBilibiliCommand(item) == cmd {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	for _, item := range cfg.ExcludeCommands {
		if normalizeBilibiliCommand(item) == cmd {
			return false
		}
	}
	return strings.HasPrefix(cmd, "DANMU_MSG")
}

func parseBilibiliDanmakuPayload(payload map[string]any, fallbackRoomID int64, provider string) (DanmakuDispatchRequest, bool) {
	command := normalizeBilibiliCommand(anyToString(payload["cmd"]))
	if !strings.HasPrefix(command, "DANMU_MSG") {
		return DanmakuDispatchRequest{}, false
	}
	info, ok := payload["info"].([]any)
	if !ok || len(info) < 3 {
		return DanmakuDispatchRequest{}, false
	}

	content := strings.TrimSpace(anyToString(info[1]))
	if content == "" {
		return DanmakuDispatchRequest{}, false
	}
	roomID := fallbackRoomID
	if data, ok := payload["data"].(map[string]any); ok {
		if parsed := anyToInt64(pickField(data, "roomid", "room_id", "roomId")); parsed > 0 {
			roomID = parsed
		}
	}
	if info0, ok := info[0].([]any); ok {
		if len(info0) > 3 {
			if parsed := anyToInt64(info0[3]); parsed > 0 {
				roomID = parsed
			}
		}
	}
	if roomID <= 0 {
		return DanmakuDispatchRequest{}, false
	}

	uid := int64(0)
	uname := ""
	if user, ok := info[2].([]any); ok {
		if len(user) > 0 {
			uid = anyToInt64(user[0])
		}
		if len(user) > 1 {
			uname = anyToString(user[1])
		}
	}
	rawBody, _ := json.Marshal(payload)
	return DanmakuDispatchRequest{
		RoomID:     roomID,
		UID:        uid,
		Uname:      uname,
		Content:    content,
		RawPayload: string(rawBody),
		Source:     "consumer." + normalizeDanmakuProvider(provider),
	}, true
}

func deriveCursorFromBilibiliPayload(payload map[string]any, index int, fallback string) string {
	if value := strings.TrimSpace(anyToString(payload["msg_id"])); value != "" {
		return value
	}
	if value := anyToInt64(payload["send_time"]); value > 0 {
		return strconv.FormatInt(value, 10)
	}
	if fallback != "" {
		return fallback + "." + strconv.Itoa(index)
	}
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func topCommandSummary(counter map[string]int, limit int) []map[string]any {
	if len(counter) == 0 {
		return []map[string]any{}
	}
	type item struct {
		Cmd   string
		Count int
	}
	list := make([]item, 0, len(counter))
	for cmd, count := range counter {
		list = append(list, item{Cmd: cmd, Count: count})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Count == list[j].Count {
			return list[i].Cmd < list[j].Cmd
		}
		return list[i].Count > list[j].Count
	})
	if limit <= 0 || limit > len(list) {
		limit = len(list)
	}
	result := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		result = append(result, map[string]any{
			"cmd":   list[i].Cmd,
			"count": list[i].Count,
		})
	}
	return result
}

func clampRange(value int, min int, max int, fallback int) int {
	if value <= 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func parseCookieValue(cookieHeader string, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	parts := strings.Split(cookieHeader, ";")
	for _, item := range parts {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		kv := strings.SplitN(item, "=", 2)
		if len(kv) != 2 {
			continue
		}
		if strings.TrimSpace(kv[0]) == key {
			return strings.TrimSpace(kv[1])
		}
	}
	return ""
}

func mergeCookiePair(cookieHeader string, key string, value string) string {
	cookieHeader = strings.TrimSpace(cookieHeader)
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return cookieHeader
	}
	if parseCookieValue(cookieHeader, key) != "" {
		return cookieHeader
	}
	if cookieHeader == "" {
		return key + "=" + value
	}
	return cookieHeader + "; " + key + "=" + value
}
