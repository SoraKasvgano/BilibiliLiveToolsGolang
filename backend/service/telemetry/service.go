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

	mu     sync.Mutex
	cancel context.CancelFunc
}

func New(storeDB *store.Store, bili bilibili.Service, streamStatusFn func() store.PushStatus) *Service {
	return &Service{
		store:    storeDB,
		bili:     bili,
		streamFn: streamStatusFn,
		interval: 30 * time.Second,
	}
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
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sample(ctx)
		}
	}
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
