package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"bilibililivetools/gover/backend/store"
)

const (
	integrationTaskTypeWebhook = "webhook"
	integrationTaskTypeBot     = "bot"
)

var queueWebhookHTTPClient = &http.Client{Timeout: 12 * time.Second}

type webhookTaskPayload struct {
	WebhookID   int64           `json:"webhookId"`
	WebhookName string          `json:"webhookName"`
	URL         string          `json:"url"`
	Secret      string          `json:"secret"`
	EventType   string          `json:"eventType"`
	Payload     json.RawMessage `json:"payload"`
}

type botTaskPayload struct {
	Provider string          `json:"provider"`
	Command  string          `json:"command"`
	Params   json.RawMessage `json:"params"`
}

type queueWebhookResult struct {
	RequestBody  string
	ResponseBody string
	ResponseCode int
	DurationMS   int64
}

func (s *Service) EnqueueWebhookTask(ctx context.Context, target store.WebhookSetting, eventType string, payload any, maxAttempts int) (int64, error) {
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureTaskQueue); err != nil || !enabled {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("feature is disabled: task_queue")
	}
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureWebhook); err != nil || !enabled {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("feature is disabled: webhook")
	}
	if strings.TrimSpace(target.URL) == "" {
		return 0, errors.New("webhook url is empty")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	taskBody, err := json.Marshal(webhookTaskPayload{
		WebhookID:   target.ID,
		WebhookName: target.Name,
		URL:         target.URL,
		Secret:      target.Secret,
		EventType:   strings.TrimSpace(eventType),
		Payload:     body,
	})
	if err != nil {
		return 0, err
	}
	rateKey := "webhook:" + strings.TrimSpace(target.Name)
	if target.ID > 0 {
		rateKey = fmt.Sprintf("webhook:%d", target.ID)
	}
	taskID, err := s.store.CreateIntegrationTask(ctx, store.IntegrationTask{
		TaskType:    integrationTaskTypeWebhook,
		Status:      store.IntegrationTaskStatusPending,
		Priority:    100,
		Payload:     string(taskBody),
		MaxAttempts: maxAttempts,
		RateKey:     rateKey,
	})
	if err != nil {
		return 0, err
	}
	_ = s.SaveLiveEventJSON(ctx, "integration.task.queued", map[string]any{
		"taskId":    taskID,
		"taskType":  integrationTaskTypeWebhook,
		"eventType": eventType,
		"webhookId": target.ID,
		"name":      target.Name,
	})
	return taskID, nil
}

func (s *Service) EnqueueBotTask(ctx context.Context, provider string, command string, params json.RawMessage, maxAttempts int) (int64, error) {
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureTaskQueue); err != nil || !enabled {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("feature is disabled: task_queue")
	}
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureBot); err != nil || !enabled {
		if err != nil {
			return 0, err
		}
		return 0, errors.New("feature is disabled: bot")
	}
	command = strings.TrimSpace(command)
	if command == "" {
		return 0, errors.New("command is required")
	}
	payload, err := json.Marshal(botTaskPayload{
		Provider: strings.TrimSpace(provider),
		Command:  command,
		Params:   params,
	})
	if err != nil {
		return 0, err
	}
	taskID, err := s.store.CreateIntegrationTask(ctx, store.IntegrationTask{
		TaskType:    integrationTaskTypeBot,
		Status:      store.IntegrationTaskStatusPending,
		Priority:    120,
		Payload:     string(payload),
		MaxAttempts: maxAttempts,
		RateKey:     "bot:" + strings.ToLower(command),
	})
	if err != nil {
		return 0, err
	}
	_ = s.SaveLiveEventJSON(ctx, "integration.task.queued", map[string]any{
		"taskId":   taskID,
		"taskType": integrationTaskTypeBot,
		"provider": provider,
		"command":  command,
	})
	return taskID, nil
}

func (s *Service) runQueueScheduler() {
	defer s.wg.Done()
	stop := s.stopChannel()
	if stop == nil {
		return
	}
	ticker := time.NewTicker(s.leaseInterval)
	defer ticker.Stop()
	defer func() {
		s.runMu.Lock()
		taskCh := s.taskCh
		s.runMu.Unlock()
		if taskCh != nil {
			close(taskCh)
		}
	}()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			featureEnabled, featureErr := s.IsFeatureEnabled(context.Background(), FeatureTaskQueue)
			if featureErr != nil {
				log.Printf("[integration][warn] read task_queue feature flag failed: %v", featureErr)
				continue
			}
			if !featureEnabled {
				s.cleanupRateKeys(30 * time.Minute)
				continue
			}
			tasks, err := s.store.LeaseIntegrationTasks(context.Background(), 48)
			if err != nil {
				log.Printf("[integration][warn] lease tasks failed: %v", err)
				continue
			}
			if len(tasks) == 0 {
				continue
			}
			for _, task := range tasks {
				s.runMu.Lock()
				taskCh := s.taskCh
				s.runMu.Unlock()
				if taskCh == nil {
					return
				}
				select {
				case <-stop:
					return
				case taskCh <- task:
				}
			}
			s.cleanupRateKeys(30 * time.Minute)
		}
	}
}

func (s *Service) runTaskWorker(_ int) {
	defer s.wg.Done()
	s.runMu.Lock()
	taskCh := s.taskCh
	stop := s.stopCh
	s.runMu.Unlock()
	if taskCh == nil || stop == nil {
		return
	}
	for {
		select {
		case <-stop:
			return
		case task, ok := <-taskCh:
			if !ok {
				return
			}
			s.processTask(task)
		}
	}
}

func (s *Service) processTask(task store.IntegrationTask) {
	attempt := task.Attempt + 1
	if attempt < 1 {
		attempt = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	s.applyRateLimit(ctx, task.RateKey, s.queueRateGap(task.TaskType))

	retryable, err := s.executeTask(ctx, task, attempt)
	if err == nil {
		_ = s.store.MarkIntegrationTaskSucceeded(context.Background(), task.ID, attempt)
		return
	}
	if !retryable || attempt >= task.MaxAttempts {
		_ = s.store.MarkIntegrationTaskDead(context.Background(), task.ID, attempt, err.Error())
		_ = s.SaveLiveEventJSON(context.Background(), "integration.task.dead", map[string]any{
			"taskId":      task.ID,
			"taskType":    task.TaskType,
			"attempt":     attempt,
			"maxAttempts": task.MaxAttempts,
			"error":       err.Error(),
		})
		return
	}
	nextRun := time.Now().UTC().Add(nextRetryDelay(attempt))
	_ = s.store.MarkIntegrationTaskRetry(context.Background(), task.ID, attempt, nextRun, err.Error())
	_ = s.SaveLiveEventJSON(context.Background(), "integration.task.retry", map[string]any{
		"taskId":   task.ID,
		"taskType": task.TaskType,
		"attempt":  attempt,
		"nextRun":  nextRun.Format(time.RFC3339),
		"error":    err.Error(),
	})
}

func (s *Service) queueRateGap(taskType string) time.Duration {
	setting := s.queueSettingCached(context.Background())
	if setting == nil {
		return 300 * time.Millisecond
	}
	switch strings.ToLower(strings.TrimSpace(taskType)) {
	case integrationTaskTypeWebhook:
		return time.Duration(setting.WebhookRateGapMS) * time.Millisecond
	case integrationTaskTypeBot:
		return time.Duration(setting.BotRateGapMS) * time.Millisecond
	default:
		return 300 * time.Millisecond
	}
}

func (s *Service) cleanupRateKeys(expireAfter time.Duration) {
	if expireAfter <= 0 {
		expireAfter = 30 * time.Minute
	}
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	for key, hitAt := range s.lastRateHit {
		if now.Sub(hitAt) > expireAfter {
			delete(s.lastRateHit, key)
		}
	}
}

func (s *Service) executeTask(ctx context.Context, task store.IntegrationTask, attempt int) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(task.TaskType)) {
	case integrationTaskTypeWebhook:
		return s.processWebhookTask(ctx, task, attempt)
	case integrationTaskTypeBot:
		return s.processBotTask(ctx, task, attempt)
	default:
		return false, errors.New("unsupported integration task type: " + task.TaskType)
	}
}

func (s *Service) processWebhookTask(ctx context.Context, task store.IntegrationTask, attempt int) (bool, error) {
	payload := webhookTaskPayload{}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return false, err
	}
	target := store.WebhookSetting{
		ID:     payload.WebhookID,
		Name:   payload.WebhookName,
		URL:    payload.URL,
		Secret: payload.Secret,
	}
	result, err := sendWebhookNow(ctx, target, payload.Payload)
	logEntry := store.WebhookDeliveryLog{
		WebhookName: target.Name,
		EventType:   payload.EventType,
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
		logEntry.Success = false
		logEntry.ErrorMessage = err.Error()
	} else {
		logEntry.Success = true
	}
	if saveErr := s.store.CreateWebhookDeliveryLog(ctx, logEntry); saveErr != nil {
		log.Printf("[integration][warn] save webhook delivery log failed: %v", saveErr)
	}
	if err != nil {
		return shouldRetryWebhookNow(result, err), err
	}
	return false, nil
}

func (s *Service) processBotTask(ctx context.Context, task store.IntegrationTask, _ int) (bool, error) {
	payload := botTaskPayload{}
	if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil {
		return false, err
	}
	result, err := s.executeBotCommandNow(ctx, payload.Provider, payload.Command, payload.Params)
	if err != nil {
		_ = s.SaveLiveEventJSON(ctx, "bot.command.error", map[string]any{
			"provider": payload.Provider,
			"command":  payload.Command,
			"error":    err.Error(),
		})
		return isRetryableBotError(err), err
	}
	_ = s.SaveLiveEventJSON(ctx, "bot.command.executed", map[string]any{
		"provider": payload.Provider,
		"command":  payload.Command,
		"result":   result,
	})
	return false, nil
}

func (s *Service) applyRateLimit(ctx context.Context, rateKey string, gap time.Duration) {
	key := strings.TrimSpace(rateKey)
	if key == "" || gap <= 0 {
		return
	}
	for {
		s.rateMu.Lock()
		last := s.lastRateHit[key]
		now := time.Now()
		wait := gap - now.Sub(last)
		if wait <= 0 {
			s.lastRateHit[key] = now
			s.rateMu.Unlock()
			return
		}
		s.rateMu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func nextRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt)) * time.Second
}

func sendWebhookNow(ctx context.Context, target store.WebhookSetting, payload json.RawMessage) (*queueWebhookResult, error) {
	if strings.TrimSpace(target.URL) == "" {
		return nil, errors.New("webhook url is empty")
	}
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		data = []byte("{}")
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gover-webhook-queue/1.0")
	if strings.TrimSpace(target.Secret) != "" {
		mac := hmac.New(sha256.New, []byte(target.Secret))
		_, _ = mac.Write(data)
		req.Header.Set("X-Gover-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := queueWebhookHTTPClient.Do(req)
	if err != nil {
		return &queueWebhookResult{
			RequestBody: string(data),
			DurationMS:  time.Since(start).Milliseconds(),
		}, err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	result := &queueWebhookResult{
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

func shouldRetryWebhookNow(result *queueWebhookResult, err error) bool {
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

func isRetryableBotError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(lower, "unsupported bot command") || strings.Contains(lower, "invalid ptz params") {
		return false
	}
	return true
}
