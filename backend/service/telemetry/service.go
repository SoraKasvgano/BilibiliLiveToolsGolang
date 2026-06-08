package telemetry

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"bilibililivetools/gover/backend/service/bilibili"
	"bilibililivetools/gover/backend/store"
)

type Service struct {
	store    *store.Store
	bili     bilibili.Service
	streamFn func() store.PushStatus
	interval time.Duration
	enabled  bool

	mu     sync.Mutex
	cancel context.CancelFunc
}

type Options struct {
	Enabled  bool
	Interval time.Duration
}

func New(storeDB *store.Store, bili bilibili.Service, streamStatusFn func() store.PushStatus, options Options) *Service {
	interval := options.Interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	return &Service{
		store:    storeDB,
		bili:     bili,
		streamFn: streamStatusFn,
		interval: interval,
		enabled:  options.Enabled,
	}
}

func (s *Service) Update(enabled bool, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enabled = enabled
	s.interval = interval
}

func (s *Service) Start() {
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.mu.Unlock()

	go s.loop(ctx)
}

func (s *Service) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *Service) loop(ctx context.Context) {
	for {
		enabled, interval := s.snapshot()
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if enabled {
				s.sample(ctx)
			}
		}
	}
}

func (s *Service) snapshot() (bool, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	interval := s.interval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return s.enabled, interval
}

func (s *Service) sample(ctx context.Context) {
	if s.streamFn == nil || s.streamFn() != store.PushStatusRunning {
		return
	}
	info, err := s.bili.GetMyLiveRoomInfo(ctx)
	if err != nil {
		log.Printf("[telemetry][warn] fetch room info failed: %v", err)
		return
	}
	payload := map[string]any{
		"room_id":     info.RoomID,
		"uid":         info.UID,
		"title":       info.Title,
		"area_v2_id":  info.AreaV2ID,
		"live_status": info.LiveStatus,
		"time":        time.Now().Format(time.RFC3339),
	}
	body, _ := json.Marshal(payload)
	if err := s.store.CreateLiveEvent(ctx, "live.room_snapshot", string(body)); err != nil {
		log.Printf("[telemetry][warn] save room snapshot failed: %v", err)
	}
}
