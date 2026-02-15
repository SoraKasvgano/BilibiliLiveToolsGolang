package integration

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"bilibililivetools/gover/backend/service/onvif"
	"bilibililivetools/gover/backend/store"
)

type StreamController interface {
	Start(ctx context.Context, startup bool) error
	Stop(ctx context.Context) error
}

type LiveStopper interface {
	StopLive(ctx context.Context, roomID int64) error
	SendDanmaku(ctx context.Context, roomID int64, message string) (map[string]any, error)
}

type PTZCommander interface {
	ExecuteCommand(ctx context.Context, req onvif.CommandRequest) (map[string]any, error)
}

type DanmakuDispatchRequest struct {
	RoomID     int64  `json:"roomId"`
	UID        int64  `json:"uid"`
	Uname      string `json:"uname"`
	Content    string `json:"content"`
	RawPayload string `json:"rawPayload"`
	Source     string `json:"source"`
}

type DanmakuDispatchResult struct {
	RoomID       int64            `json:"roomId"`
	Content      string           `json:"content"`
	MatchedCount int              `json:"matchedCount"`
	Executed     []map[string]any `json:"executed"`
	Failed       []map[string]any `json:"failed"`
}

type DanmakuConsumerRuntime struct {
	Running       bool       `json:"running"`
	LastPollAt    *time.Time `json:"lastPollAt,omitempty"`
	LastCursor    string     `json:"lastCursor"`
	LastError     string     `json:"lastError"`
	LastFetched   int        `json:"lastFetched"`
	LastProcessed int        `json:"lastProcessed"`
	LastMatched   int        `json:"lastMatched"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type Service struct {
	store  *store.Store
	stream StreamController
	bili   LiveStopper
	onvif  PTZCommander

	runMu         sync.Mutex
	running       bool
	stopCh        chan struct{}
	doneCh        chan struct{}
	taskCh        chan store.IntegrationTask
	workerCount   int
	leaseInterval time.Duration
	wg            sync.WaitGroup

	rateMu      sync.Mutex
	lastRateHit map[string]time.Time

	consumerMu    sync.RWMutex
	consumerState DanmakuConsumerRuntime

	queueCfgMu     sync.RWMutex
	queueCfgCache  *store.IntegrationQueueSetting
	queueCfgExpire time.Time
}

type FeatureName string

const (
	FeatureDanmakuConsumer FeatureName = "danmaku_consumer"
	FeatureWebhook         FeatureName = "webhook"
	FeatureBot             FeatureName = "bot"
	FeatureAdvancedStats   FeatureName = "advanced_stats"
	FeatureTaskQueue       FeatureName = "task_queue"
)

func New(storeDB *store.Store, stream StreamController, bili LiveStopper, ptz PTZCommander) *Service {
	return &Service{
		store:         storeDB,
		stream:        stream,
		bili:          bili,
		onvif:         ptz,
		workerCount:   3,
		leaseInterval: 500 * time.Millisecond,
		lastRateHit:   make(map[string]time.Time),
		consumerState: DanmakuConsumerRuntime{},
	}
}

func (s *Service) Start() {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.running {
		return
	}
	if queueCfg := s.queueSettingCached(context.Background()); queueCfg != nil {
		if queueCfg.MaxWorkers > 0 {
			s.workerCount = queueCfg.MaxWorkers
		}
		if queueCfg.LeaseIntervalMS >= 100 {
			s.leaseInterval = time.Duration(queueCfg.LeaseIntervalMS) * time.Millisecond
		}
	}
	if s.workerCount <= 0 {
		s.workerCount = 3
	}
	if s.workerCount > 16 {
		s.workerCount = 16
	}
	if s.leaseInterval < 100*time.Millisecond {
		s.leaseInterval = 500 * time.Millisecond
	}
	s.stopCh = make(chan struct{})
	s.doneCh = make(chan struct{})
	s.taskCh = make(chan store.IntegrationTask, 128)
	s.running = true

	s.wg = sync.WaitGroup{}
	s.wg.Add(1 + s.workerCount + 1)
	go s.runQueueScheduler()
	for i := 0; i < s.workerCount; i++ {
		go s.runTaskWorker(i + 1)
	}
	go s.runDanmakuConsumerLoop()
}

func (s *Service) Stop() {
	s.runMu.Lock()
	if !s.running {
		s.runMu.Unlock()
		return
	}
	stop := s.stopCh
	done := s.doneCh
	s.running = false
	s.stopCh = nil
	s.doneCh = nil
	s.runMu.Unlock()

	close(stop)
	s.wg.Wait()
	s.runMu.Lock()
	s.taskCh = nil
	s.runMu.Unlock()
	close(done)
}

func (s *Service) isRunning() bool {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.running
}

func (s *Service) stopChannel() <-chan struct{} {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.stopCh
}

func (s *Service) markConsumerState(update func(*DanmakuConsumerRuntime)) {
	s.consumerMu.Lock()
	defer s.consumerMu.Unlock()
	update(&s.consumerState)
	s.consumerState.UpdatedAt = time.Now().UTC()
}

func (s *Service) ConsumerRuntime() DanmakuConsumerRuntime {
	s.consumerMu.RLock()
	defer s.consumerMu.RUnlock()
	state := s.consumerState
	return state
}

func (s *Service) ListDanmakuRules(ctx context.Context, limit int, offset int) ([]store.DanmakuPTZRule, error) {
	return s.store.ListDanmakuRules(ctx, limit, offset)
}

func (s *Service) SaveDanmakuRule(ctx context.Context, rule store.DanmakuPTZRule) error {
	return s.store.SaveDanmakuRule(ctx, rule)
}

func (s *Service) ListWebhooks(ctx context.Context, limit int, offset int) ([]store.WebhookSetting, error) {
	return s.store.ListWebhooks(ctx, limit, offset)
}

func (s *Service) SaveWebhook(ctx context.Context, item store.WebhookSetting) error {
	return s.store.SaveWebhook(ctx, item)
}

func (s *Service) CreateWebhookDeliveryLog(ctx context.Context, item store.WebhookDeliveryLog) error {
	return s.store.CreateWebhookDeliveryLog(ctx, item)
}

func (s *Service) ListWebhookDeliveryLogs(ctx context.Context, limit int, webhookID int64) ([]store.WebhookDeliveryLog, error) {
	return s.store.ListWebhookDeliveryLogs(ctx, limit, webhookID)
}

func (s *Service) GetBilibiliAPIAlertSetting(ctx context.Context) (*store.BilibiliAPIAlertSetting, error) {
	return s.store.GetBilibiliAPIAlertSetting(ctx)
}

func (s *Service) SaveBilibiliAPIAlertSetting(ctx context.Context, req store.BilibiliAPIAlertSetting) (*store.BilibiliAPIAlertSetting, error) {
	return s.store.SaveBilibiliAPIAlertSetting(ctx, req)
}

func (s *Service) MarkBilibiliAPIAlertSent(ctx context.Context, settingID int64, sentAt time.Time) error {
	return s.store.MarkBilibiliAPIAlertSent(ctx, settingID, sentAt)
}

func (s *Service) CreateBilibiliAPIErrorLog(ctx context.Context, item store.BilibiliAPIErrorLog) (int64, error) {
	return s.store.CreateBilibiliAPIErrorLog(ctx, item)
}

func (s *Service) ListBilibiliAPIErrorLogs(ctx context.Context, limit int, endpointKeyword string) ([]store.BilibiliAPIErrorLog, error) {
	return s.store.ListBilibiliAPIErrorLogs(ctx, limit, endpointKeyword)
}

func (s *Service) GetBilibiliAPIErrorLog(ctx context.Context, id int64) (*store.BilibiliAPIErrorLog, error) {
	return s.store.GetBilibiliAPIErrorLog(ctx, id)
}

func (s *Service) ListAPIKeys(ctx context.Context, limit int, offset int) ([]store.APIKeySetting, error) {
	return s.store.ListAPIKeys(ctx, limit, offset)
}

func (s *Service) SaveAPIKey(ctx context.Context, item store.APIKeySetting) error {
	return s.store.SaveAPIKey(ctx, item)
}

func (s *Service) SaveLiveEvent(ctx context.Context, eventType string, payload string) error {
	return s.store.CreateLiveEvent(ctx, eventType, payload)
}

func (s *Service) SaveLiveEventJSON(ctx context.Context, eventType string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return s.store.CreateLiveEvent(ctx, eventType, string(body))
}

func (s *Service) ListLiveEvents(ctx context.Context, limit int) ([]store.LiveEvent, error) {
	return s.store.ListLiveEvents(ctx, limit)
}

func (s *Service) ListLiveEventsByType(ctx context.Context, eventType string, limit int) ([]store.LiveEvent, error) {
	return s.store.ListLiveEventsByType(ctx, eventType, limit)
}

func (s *Service) CountLiveEventsByTypeSince(ctx context.Context, eventType string, since time.Time) (int64, error) {
	return s.store.CountLiveEventsByTypeSince(ctx, eventType, since)
}

func (s *Service) CountLiveEventsSince(ctx context.Context, since time.Time) (int64, error) {
	return s.store.CountLiveEventsSince(ctx, since)
}

func (s *Service) ListLiveEventsByTypeSince(ctx context.Context, eventType string, since time.Time, limit int) ([]store.LiveEvent, error) {
	return s.store.ListLiveEventsByTypeSince(ctx, eventType, since, limit)
}

func (s *Service) InsertDanmakuRecord(ctx context.Context, record store.DanmakuRecord) error {
	return s.store.InsertDanmakuRecord(ctx, record)
}

func (s *Service) ListDanmakuRecords(ctx context.Context, roomID int64, limit int) ([]store.DanmakuRecord, error) {
	return s.store.ListDanmakuRecords(ctx, roomID, limit)
}

func (s *Service) CountDanmakuRecordsSince(ctx context.Context, roomID int64, since time.Time) (int64, error) {
	return s.store.CountDanmakuRecordsSince(ctx, roomID, since)
}

func (s *Service) GetDanmakuConsumerSetting(ctx context.Context) (*store.DanmakuConsumerSetting, error) {
	return s.store.GetDanmakuConsumerSetting(ctx)
}

func (s *Service) SaveDanmakuConsumerSetting(ctx context.Context, req store.DanmakuConsumerSetting) (*store.DanmakuConsumerSetting, error) {
	return s.store.SaveDanmakuConsumerSetting(ctx, req)
}

func (s *Service) GetFeatureSetting(ctx context.Context) (*store.IntegrationFeatureSetting, error) {
	return s.store.GetIntegrationFeatureSetting(ctx)
}

func (s *Service) SaveFeatureSetting(ctx context.Context, req store.IntegrationFeatureSetting) (*store.IntegrationFeatureSetting, error) {
	return s.store.SaveIntegrationFeatureSetting(ctx, req)
}

func (s *Service) IsFeatureEnabled(ctx context.Context, feature FeatureName) (bool, error) {
	setting, err := s.store.GetIntegrationFeatureSetting(ctx)
	if err != nil {
		return false, err
	}
	return resolveFeatureEnabled(setting, feature), nil
}

func (s *Service) EnsureFeatureEnabled(ctx context.Context, feature FeatureName) error {
	enabled, err := s.IsFeatureEnabled(ctx, feature)
	if err != nil {
		return err
	}
	if enabled {
		return nil
	}
	return errors.New("feature is disabled: " + string(feature))
}

func (s *Service) ListIntegrationTasks(ctx context.Context, limit int, status string, taskType string) ([]store.IntegrationTask, error) {
	return s.store.ListIntegrationTasks(ctx, limit, status, taskType)
}

func (s *Service) IntegrationTaskSummary(ctx context.Context) (store.IntegrationTaskSummary, error) {
	return s.store.IntegrationTaskSummary(ctx)
}

func (s *Service) RetryIntegrationTask(ctx context.Context, id int64) error {
	return s.store.RetryIntegrationTask(ctx, id)
}

func (s *Service) CancelIntegrationTask(ctx context.Context, id int64) error {
	return s.store.CancelIntegrationTask(ctx, id)
}

func (s *Service) UpdateIntegrationTaskPriority(ctx context.Context, id int64, priority int) error {
	return s.store.UpdateIntegrationTaskPriority(ctx, id, priority)
}

func (s *Service) RetryIntegrationTasksBatch(ctx context.Context, ids []int64, status string, taskType string, limit int) (int64, error) {
	return s.store.RetryIntegrationTasksBatch(ctx, ids, status, taskType, limit)
}

func (s *Service) GetQueueSetting(ctx context.Context) (*store.IntegrationQueueSetting, error) {
	return s.store.GetIntegrationQueueSetting(ctx)
}

func (s *Service) SaveQueueSetting(ctx context.Context, req store.IntegrationQueueSetting) (*store.IntegrationQueueSetting, error) {
	item, err := s.store.SaveIntegrationQueueSetting(ctx, req)
	if err != nil {
		return nil, err
	}
	s.queueCfgMu.Lock()
	s.queueCfgCache = item
	s.queueCfgExpire = time.Now().Add(10 * time.Second)
	s.queueCfgMu.Unlock()
	return item, nil
}

func (s *Service) queueSettingCached(ctx context.Context) *store.IntegrationQueueSetting {
	s.queueCfgMu.RLock()
	cache := s.queueCfgCache
	expire := s.queueCfgExpire
	s.queueCfgMu.RUnlock()
	if cache != nil && time.Now().Before(expire) {
		copyValue := *cache
		return &copyValue
	}
	item, err := s.store.GetIntegrationQueueSetting(ctx)
	if err != nil || item == nil {
		return nil
	}
	s.queueCfgMu.Lock()
	s.queueCfgCache = item
	s.queueCfgExpire = time.Now().Add(10 * time.Second)
	s.queueCfgMu.Unlock()
	copyValue := *item
	return &copyValue
}

func resolveFeatureEnabled(setting *store.IntegrationFeatureSetting, feature FeatureName) bool {
	if setting == nil {
		return false
	}
	if setting.SimpleMode {
		return false
	}
	switch feature {
	case FeatureDanmakuConsumer:
		return setting.EnableDanmakuConsumer
	case FeatureWebhook:
		return setting.EnableWebhook
	case FeatureBot:
		return setting.EnableBot
	case FeatureAdvancedStats:
		return setting.EnableAdvancedStats
	case FeatureTaskQueue:
		return setting.EnableTaskQueue
	default:
		return false
	}
}
