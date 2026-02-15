package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"bilibililivetools/gover/backend/service/onvif"
	"bilibililivetools/gover/backend/store"
)

func (s *Service) DispatchDanmaku(ctx context.Context, req DanmakuDispatchRequest) (*DanmakuDispatchResult, error) {
	if req.RoomID <= 0 {
		return nil, errors.New("roomId is required")
	}
	req.Content = strings.TrimSpace(req.Content)
	if req.Content == "" {
		return nil, errors.New("content is required")
	}
	record := store.DanmakuRecord{
		RoomID:     req.RoomID,
		UID:        req.UID,
		Uname:      strings.TrimSpace(req.Uname),
		Content:    req.Content,
		RawPayload: req.RawPayload,
	}
	if err := s.store.InsertDanmakuRecord(ctx, record); err != nil {
		return nil, err
	}

	rules, err := s.store.ListDanmakuRules(ctx, 2000, 0)
	if err != nil {
		return nil, err
	}
	pushSetting, _ := s.store.GetPushSetting(ctx)
	contentLower := strings.ToLower(req.Content)

	result := &DanmakuDispatchResult{
		RoomID:   req.RoomID,
		Content:  req.Content,
		Executed: make([]map[string]any, 0, 8),
		Failed:   make([]map[string]any, 0, 4),
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		keyword := strings.ToLower(strings.TrimSpace(rule.Keyword))
		if keyword == "" || !strings.Contains(contentLower, keyword) {
			continue
		}
		result.MatchedCount++

		execResult, execErr := s.executeRule(ctx, rule, req, pushSetting)
		eventPayload := map[string]any{
			"ruleId":       rule.ID,
			"keyword":      rule.Keyword,
			"action":       rule.Action,
			"ptzDirection": rule.PTZDirection,
			"source":       defaultString(req.Source, "manual"),
			"roomId":       req.RoomID,
			"uid":          req.UID,
			"uname":        req.Uname,
			"content":      req.Content,
			"result":       execResult,
		}
		if execErr != nil {
			eventPayload["error"] = execErr.Error()
			result.Failed = append(result.Failed, map[string]any{
				"ruleId":  rule.ID,
				"keyword": rule.Keyword,
				"error":   execErr.Error(),
			})
			_ = s.SaveLiveEventJSON(ctx, "danmaku.rule.error", eventPayload)
			continue
		}
		result.Executed = append(result.Executed, map[string]any{
			"ruleId":  rule.ID,
			"keyword": rule.Keyword,
			"action":  rule.Action,
			"result":  execResult,
		})
		_ = s.SaveLiveEventJSON(ctx, "danmaku.rule.executed", eventPayload)
	}
	return result, nil
}

func (s *Service) executeRule(ctx context.Context, rule store.DanmakuPTZRule, req DanmakuDispatchRequest, pushSetting *store.PushSetting) (map[string]any, error) {
	action := strings.ToLower(strings.TrimSpace(rule.Action))
	if action == "" {
		action = "ptz"
	}
	switch action {
	case "ptz":
		if s.onvif == nil {
			return nil, errors.New("ptz runtime is unavailable")
		}
		if pushSetting == nil {
			return nil, errors.New("ptz rule failed: push setting not found")
		}
		speed := float64(rule.PTZSpeed) / 10
		if speed <= 0 {
			speed = 0.3
		}
		if speed > 1 {
			speed = 1
		}
		return s.onvif.ExecuteCommand(ctx, onvif.CommandRequest{
			Endpoint:     pushSetting.ONVIFEndpoint,
			Username:     pushSetting.ONVIFUsername,
			Password:     pushSetting.ONVIFPassword,
			ProfileToken: pushSetting.ONVIFProfileToken,
			Action:       normalizePTZAction(rule.PTZDirection),
			Speed:        speed,
			DurationMS:   700,
		})
	case "start_live":
		if s.stream == nil {
			return nil, errors.New("stream runtime is unavailable")
		}
		if err := s.stream.Start(ctx, false); err != nil {
			return nil, err
		}
		return map[string]any{"started": true}, nil
	case "stop_live":
		if s.stream != nil {
			_ = s.stream.Stop(ctx)
		}
		if s.bili != nil {
			if room, err := s.store.GetLiveSetting(ctx); err == nil && room.RoomID > 0 {
				_ = s.bili.StopLive(ctx, room.RoomID)
			}
		}
		return map[string]any{"stopped": true}, nil
	case "webhook":
		webhooks, err := s.store.ListWebhooks(ctx, 1000, 0)
		if err != nil {
			return nil, err
		}
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
		taskIDs := make([]int64, 0, len(webhooks))
		failed := make([]string, 0)
		for _, item := range webhooks {
			if !item.Enabled {
				continue
			}
			taskID, queueErr := s.EnqueueWebhookTask(ctx, item, "danmaku.rule.webhook", payload, 3)
			if queueErr != nil {
				failed = append(failed, item.Name+": "+queueErr.Error())
				continue
			}
			taskIDs = append(taskIDs, taskID)
		}
		return map[string]any{
			"queued":  len(taskIDs),
			"taskIds": taskIDs,
			"failed":  failed,
		}, nil
	default:
		return nil, errors.New("unsupported rule action: " + action)
	}
}

func (s *Service) executeBotCommandNow(ctx context.Context, provider string, command string, params json.RawMessage) (map[string]any, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return nil, errors.New("command is required")
	}
	paramsMap := map[string]any{}
	if len(strings.TrimSpace(string(params))) > 0 {
		if err := json.Unmarshal(params, &paramsMap); err != nil {
			return nil, errors.New("invalid ptz params")
		}
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	commandResult := map[string]any{
		"provider": provider,
		"command":  command,
		"status":   "accepted",
	}
	switch command {
	case "start_live":
		if s.stream == nil {
			return nil, errors.New("stream runtime is unavailable")
		}
		if err := s.stream.Start(ctx, false); err != nil {
			return nil, err
		}
		commandResult["live"] = map[string]any{"started": true}
	case "stop_live":
		if s.stream != nil {
			_ = s.stream.Stop(ctx)
		}
		if s.bili != nil {
			if room, err := s.store.GetLiveSetting(ctx); err == nil && room.RoomID > 0 {
				_ = s.bili.StopLive(ctx, room.RoomID)
			}
		}
		commandResult["live"] = map[string]any{"stopped": true}
	case "ptz":
		if s.onvif == nil {
			return nil, errors.New("ptz runtime is unavailable")
		}
		pushSetting, _ := s.store.GetPushSetting(ctx)
		commandReq := onvif.CommandRequest{
			Endpoint:     defaultString(asString(paramsMap["endpoint"]), pushSetting.ONVIFEndpoint),
			Username:     defaultString(asString(paramsMap["username"]), pushSetting.ONVIFUsername),
			Password:     defaultString(asString(paramsMap["password"]), pushSetting.ONVIFPassword),
			ProfileToken: defaultString(asString(paramsMap["profileToken"]), pushSetting.ONVIFProfileToken),
			Action:       defaultString(asString(paramsMap["action"]), "stop"),
			Direction:    asString(paramsMap["direction"]),
			Speed:        onvif.ParseFloatOrDefault(paramsMap["speed"], 0.3),
			DurationMS:   int(onvif.ParseFloatOrDefault(paramsMap["durationMs"], 700)),
			Pan:          onvif.ParseFloatOrDefault(paramsMap["pan"], 0),
			Tilt:         onvif.ParseFloatOrDefault(paramsMap["tilt"], 0),
			Zoom:         onvif.ParseFloatOrDefault(paramsMap["zoom"], 0),
		}
		ptzResult, err := s.onvif.ExecuteCommand(ctx, commandReq)
		if err != nil {
			return nil, err
		}
		commandResult["ptz"] = ptzResult
	case "send_danmaku":
		if s.bili == nil {
			return nil, errors.New("bilibili runtime is unavailable")
		}
		message := defaultString(asString(paramsMap["message"]), "")
		if message == "" {
			message = defaultString(asString(paramsMap["content"]), "")
		}
		if message == "" {
			message = defaultString(asString(paramsMap["text"]), "")
		}
		if strings.TrimSpace(message) == "" {
			return nil, errors.New("message is required for send_danmaku")
		}
		roomID := parseInt64(paramsMap["roomId"])
		result, err := s.bili.SendDanmaku(ctx, roomID, message)
		if err != nil {
			if provider != "" && shouldNotifyProvider(provider, command, paramsMap) {
				_, _ = s.sendProviderMessage(ctx, provider, paramsMap,
					"[Gover] send_danmaku failed",
					"error="+err.Error()+" message="+message)
			}
			return nil, err
		}
		commandResult["sendDanmaku"] = result
	case "provider_notify":
		title := defaultString(asString(paramsMap["title"]), "[Gover] provider notify")
		content := defaultString(asString(paramsMap["content"]), asString(paramsMap["message"]))
		if strings.TrimSpace(content) == "" {
			return nil, errors.New("content is required for provider_notify")
		}
		notifyResult, err := s.sendProviderMessage(ctx, provider, paramsMap, title, content)
		if err != nil {
			return nil, err
		}
		commandResult["providerNotify"] = notifyResult
	default:
		return nil, errors.New("unsupported bot command")
	}
	if shouldNotifyProvider(provider, command, paramsMap) {
		title := "[Gover] bot command: " + command
		content := buildCommandNotifyContent(commandResult)
		notifyResult, err := s.sendProviderMessage(ctx, provider, paramsMap, title, content)
		if err != nil {
			commandResult["providerNotifyError"] = err.Error()
		} else {
			commandResult["providerNotify"] = notifyResult
		}
	}
	return commandResult, nil
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

func parseInt64(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
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

func asBool(value any, fallback bool) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		default:
			return fallback
		}
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return fallback
	}
}

func shouldNotifyProvider(provider string, command string, params map[string]any) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	if command == "provider_notify" {
		return false
	}
	if command == "send_danmaku" {
		if _, exists := params["notifyResult"]; !exists {
			return true
		}
	}
	return asBool(params["notifyResult"], false)
}

func buildCommandNotifyContent(result map[string]any) string {
	body, err := json.Marshal(result)
	if err != nil {
		return "bot command completed"
	}
	return truncateText(string(body), 1200)
}
