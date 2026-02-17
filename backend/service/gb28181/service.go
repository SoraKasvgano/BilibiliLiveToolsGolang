package gb28181

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/store"
)

const (
	defaultSipVersion = "SIP/2.0"
	userAgent         = "BilibiliLiveToolsGover-GB28181/1.0"
	minSweepInterval  = 5 * time.Second
	maxSweepInterval  = 30 * time.Second
)

type RuntimeStatus struct {
	Running         bool       `json:"running"`
	BindAddrs       []string   `json:"bindAddrs"`
	Transport       string     `json:"transport"`
	Received        int64      `json:"received"`
	Sent            int64      `json:"sent"`
	LastPacketAt    *time.Time `json:"lastPacketAt,omitempty"`
	LastError       string     `json:"lastError"`
	MediaPortStart  int        `json:"mediaPortStart"`
	MediaPortEnd    int        `json:"mediaPortEnd"`
	MediaPortUsed   int        `json:"mediaPortUsed"`
	MediaPortFree   int        `json:"mediaPortFree"`
	MediaPortLeases []int      `json:"mediaPortLeases,omitempty"`
}

type InviteRequest struct {
	DeviceID  string `json:"deviceId"`
	ChannelID string `json:"channelId"`
	MediaIP   string `json:"mediaIp"`
	MediaPort int    `json:"mediaPort"`
	SSRC      string `json:"ssrc"`
	StreamID  string `json:"streamId"`
}

type SessionSDPResult struct {
	CallID    string `json:"callId"`
	DeviceID  string `json:"deviceId"`
	ChannelID string `json:"channelId"`
	MediaIP   string `json:"mediaIp"`
	MediaPort int    `json:"mediaPort"`
	SSRC      string `json:"ssrc"`
	SDP       string `json:"sdp"`
	SDPPath   string `json:"sdpPath"`
}

type nonceEntry struct {
	Nonce     string
	ExpiresAt time.Time
}

type tcpPeer struct {
	conn      net.Conn
	mu        sync.Mutex
	remote    string
	lastSeen  time.Time
	deviceIDs map[string]struct{}
}

type Service struct {
	store *store.Store

	mu          sync.RWMutex
	cfg         config.Config
	running     bool
	bindAddrs   []string
	udpConn     *net.UDPConn
	tcpListener net.Listener
	stopCh      chan struct{}
	wg          sync.WaitGroup
	lastPacket  time.Time
	lastError   string

	received int64
	sent     int64

	nonceMu sync.Mutex
	nonces  map[string]nonceEntry

	tcpPeersMu sync.RWMutex
	tcpPeers   map[string]*tcpPeer

	mediaPortMu     sync.Mutex
	mediaPortByCall map[string]int
	mediaCallByPort map[int]string
	mediaPortCursor int
}

func New(storeDB *store.Store, cfg config.Config) *Service {
	return &Service{
		store:           storeDB,
		cfg:             cfg,
		nonces:          make(map[string]nonceEntry),
		tcpPeers:        make(map[string]*tcpPeer),
		mediaPortByCall: make(map[string]int),
		mediaCallByPort: make(map[int]string),
	}
}

func (s *Service) UpdateConfig(cfg config.Config) {
	s.mu.Lock()
	oldCfg := s.cfg
	wasRunning := s.running
	s.cfg = cfg
	s.mu.Unlock()

	if !wasRunning {
		return
	}
	if !gbConfigEqual(oldCfg, cfg) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Stop(ctx); err != nil {
			log.Printf("[gb28181][warn] stop on config update failed: %v", err)
		}
		if cfg.GB28181Enabled {
			if err := s.Start(ctx); err != nil {
				log.Printf("[gb28181][warn] restart on config update failed: %v", err)
			}
		}
	}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	cfg := s.cfg
	if !cfg.GB28181Enabled {
		s.mu.Unlock()
		return errors.New("gb28181 is disabled in config")
	}
	s.stopCh = make(chan struct{})
	s.bindAddrs = nil
	s.lastError = ""
	s.lastPacket = time.Time{}
	atomic.StoreInt64(&s.received, 0)
	atomic.StoreInt64(&s.sent, 0)
	s.resetMediaPortAllocations(cfg)
	s.running = true
	s.mu.Unlock()

	var startErr error
	transport := strings.ToLower(strings.TrimSpace(cfg.GB28181Transport))
	if transport == "" {
		transport = "udp"
	}
	switch transport {
	case "udp":
		startErr = s.startUDP(cfg)
	case "tcp":
		startErr = s.startTCP(cfg)
	case "both":
		if err := s.startUDP(cfg); err != nil {
			startErr = err
		}
		if err := s.startTCP(cfg); err != nil {
			if startErr == nil {
				startErr = err
			} else {
				startErr = fmt.Errorf("%v; %w", startErr, err)
			}
		}
	default:
		startErr = fmt.Errorf("unsupported gb28181 transport: %s", transport)
	}
	if startErr != nil {
		_ = s.Stop(context.Background())
		return startErr
	}
	s.syncMediaPortAllocations(context.Background())
	s.startHousekeeping(cfg)
	_ = ctx
	log.Printf("[gb28181] service started transport=%s bind=%v", transport, s.Status().BindAddrs)
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	stopCh := s.stopCh
	udpConn := s.udpConn
	tcpListener := s.tcpListener
	s.running = false
	s.udpConn = nil
	s.tcpListener = nil
	s.stopCh = nil
	s.bindAddrs = nil
	s.mu.Unlock()

	if stopCh != nil {
		close(stopCh)
	}
	if udpConn != nil {
		_ = udpConn.Close()
	}
	if tcpListener != nil {
		_ = tcpListener.Close()
	}
	s.closeAllTCPPeers()
	s.resetMediaPortAllocations(s.currentConfig())

	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	log.Printf("[gb28181] service stopped")
	return nil
}

func (s *Service) Status() RuntimeStatus {
	s.mu.RLock()
	lastPacket := s.lastPacket
	cfg := s.cfg
	status := RuntimeStatus{
		Running:        s.running,
		BindAddrs:      append([]string(nil), s.bindAddrs...),
		Transport:      s.cfg.GB28181Transport,
		Received:       atomic.LoadInt64(&s.received),
		Sent:           atomic.LoadInt64(&s.sent),
		LastError:      s.lastError,
		MediaPortStart: cfg.GB28181MediaPortStart,
		MediaPortEnd:   cfg.GB28181MediaPortEnd,
	}
	s.mu.RUnlock()
	used, leases := s.mediaPortSnapshot()
	status.MediaPortUsed = used
	status.MediaPortLeases = leases
	totalPorts := 0
	if status.MediaPortEnd >= status.MediaPortStart && status.MediaPortStart > 0 {
		totalPorts = status.MediaPortEnd - status.MediaPortStart + 1
	}
	if totalPorts > status.MediaPortUsed {
		status.MediaPortFree = totalPorts - status.MediaPortUsed
	}
	if !lastPacket.IsZero() {
		t := lastPacket
		status.LastPacketAt = &t
	}
	return status
}

func (s *Service) startHousekeeping(cfg config.Config) {
	interval := time.Duration(cfg.GB28181HeartbeatInterval/2) * time.Second
	if interval < minSweepInterval {
		interval = minSweepInterval
	}
	if interval > maxSweepInterval {
		interval = maxSweepInterval
	}
	stopCh := s.stopCh
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				s.runHousekeeping()
			}
		}
	}()
}

func (s *Service) runHousekeeping() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := s.currentConfig()
	if err := s.markStaleDevicesOffline(ctx, cfg); err != nil {
		s.setLastError(err)
	}

	ackTimeout := cfg.GB28181AckTimeoutSec
	if ackTimeout <= 0 {
		ackTimeout = 10
	}
	before := time.Now().UTC().Add(-time.Duration(ackTimeout) * time.Second)
	affected, err := s.failTimedOutInvitingSessions(ctx, before)
	if err != nil {
		s.setLastError(err)
	} else if affected > 0 {
		log.Printf("[gb28181] housekeeping marked %d inviting session(s) as failed", affected)
	}

	s.nonceMu.Lock()
	s.cleanupNonceLocked()
	s.nonceMu.Unlock()
}

func (s *Service) markStaleDevicesOffline(ctx context.Context, cfg config.Config) error {
	page := 1
	const pageSize = 200
	now := time.Now().UTC()
	offlineIDs := make([]int64, 0, 32)

	for {
		result, err := s.store.ListGB28181Devices(ctx, store.GB28181DeviceListRequest{
			Status: string(store.GB28181DeviceStatusOnline),
			Page:   page,
			Limit:  pageSize,
		})
		if err != nil {
			return err
		}
		if len(result.Data) == 0 {
			break
		}

		for _, device := range result.Data {
			lastSeen := device.UpdatedAt
			if device.LastRegisterAt != nil && device.LastRegisterAt.After(lastSeen) {
				lastSeen = *device.LastRegisterAt
			}
			if device.LastKeepaliveAt != nil && device.LastKeepaliveAt.After(lastSeen) {
				lastSeen = *device.LastKeepaliveAt
			}

			ttl := time.Duration(device.Expires) * time.Second
			if ttl <= 0 {
				ttl = time.Duration(cfg.GB28181RegisterExpires) * time.Second
			}
			if ttl < 30*time.Second {
				ttl = 30 * time.Second
			}
			heartbeatGrace := time.Duration(cfg.GB28181HeartbeatInterval*3) * time.Second
			if heartbeatGrace > ttl {
				ttl = heartbeatGrace
			}
			if now.Sub(lastSeen) > ttl {
				offlineIDs = append(offlineIDs, device.ID)
			}
		}

		if int64(page*pageSize) >= result.DataCount || len(result.Data) < pageSize {
			break
		}
		page++
	}

	if len(offlineIDs) == 0 {
		return nil
	}
	affected, err := s.store.BatchUpdateGB28181DeviceStatus(ctx, offlineIDs, store.GB28181DeviceStatusOffline)
	if err != nil {
		return err
	}
	log.Printf("[gb28181] housekeeping marked %d stale device(s) offline", affected)
	return nil
}

func (s *Service) failTimedOutInvitingSessions(ctx context.Context, before time.Time) (int64, error) {
	sessions, err := s.store.ListGB28181Sessions(ctx, 1000, string(store.GB28181SessionStatusInviting))
	if err != nil {
		return 0, err
	}
	var affected int64
	for _, session := range sessions {
		if session.UpdatedAt.After(before) {
			continue
		}
		_, upsertErr := s.store.UpsertGB28181Session(ctx, store.GB28181SessionUpsertRequest{
			DeviceID:   session.DeviceID,
			ChannelID:  session.ChannelID,
			CallID:     session.CallID,
			Branch:     session.Branch,
			StreamID:   session.StreamID,
			RemoteAddr: session.RemoteAddr,
			Status:     store.GB28181SessionStatusFailed,
		})
		if upsertErr != nil {
			log.Printf("[gb28181][warn] timeout mark failed callId=%s: %v", session.CallID, upsertErr)
			continue
		}
		s.releaseMediaPortByCallID(session.CallID)
		affected++
	}
	return affected, nil
}

func (s *Service) currentConfig() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Service) resetMediaPortAllocations(cfg config.Config) {
	start, _ := normalizeMediaPortRange(cfg)
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	s.mediaPortByCall = make(map[string]int)
	s.mediaCallByPort = make(map[int]string)
	s.mediaPortCursor = start
}

func (s *Service) mediaPortSnapshot() (int, []int) {
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	leases := make([]int, 0, len(s.mediaCallByPort))
	for port := range s.mediaCallByPort {
		leases = append(leases, port)
	}
	if len(leases) > 1 {
		sortInts(leases)
	}
	return len(leases), leases
}

func (s *Service) allocateMediaPort(callID string) (int, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return 0, errors.New("callId is required")
	}
	cfg := s.currentConfig()
	start, end := normalizeMediaPortRange(cfg)
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	if port, ok := s.mediaPortByCall[callID]; ok && port > 0 {
		return port, nil
	}
	if s.mediaPortCursor < start || s.mediaPortCursor > end {
		s.mediaPortCursor = start
	}
	candidates := end - start + 1
	if candidates <= 0 {
		return 0, errors.New("invalid media port range")
	}
	for i := 0; i < candidates; i++ {
		port := s.mediaPortCursor
		s.mediaPortCursor++
		if s.mediaPortCursor > end {
			s.mediaPortCursor = start
		}
		if _, used := s.mediaCallByPort[port]; used {
			continue
		}
		s.mediaPortByCall[callID] = port
		s.mediaCallByPort[port] = callID
		return port, nil
	}
	return 0, errors.New("media port pool exhausted")
}

func (s *Service) reserveMediaPort(callID string, port int) error {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return errors.New("callId is required")
	}
	if port <= 0 {
		return errors.New("invalid media port")
	}
	cfg := s.currentConfig()
	start, end := normalizeMediaPortRange(cfg)
	if port < start || port > end {
		return fmt.Errorf("media port %d is out of pool range %d-%d", port, start, end)
	}
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	if existing, ok := s.mediaPortByCall[callID]; ok {
		if existing == port {
			return nil
		}
		delete(s.mediaCallByPort, existing)
	}
	if owner, exists := s.mediaCallByPort[port]; exists && owner != callID {
		return fmt.Errorf("media port %d is already occupied by callId=%s", port, owner)
	}
	s.mediaPortByCall[callID] = port
	s.mediaCallByPort[port] = callID
	if s.mediaPortCursor == 0 || s.mediaPortCursor < start || s.mediaPortCursor > end {
		s.mediaPortCursor = start
	}
	return nil
}

func (s *Service) releaseMediaPortByCallID(callID string) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	if port, ok := s.mediaPortByCall[callID]; ok {
		delete(s.mediaPortByCall, callID)
		delete(s.mediaCallByPort, port)
	}
}

func (s *Service) syncMediaPortAllocations(ctx context.Context) {
	cfg := s.currentConfig()
	start, end := normalizeMediaPortRange(cfg)
	sessions, err := s.store.ListGB28181Sessions(ctx, 1000, "")
	if err != nil {
		log.Printf("[gb28181][warn] media port sync skipped: %v", err)
		return
	}
	s.mediaPortMu.Lock()
	defer s.mediaPortMu.Unlock()
	s.mediaPortByCall = make(map[string]int, len(sessions))
	s.mediaCallByPort = make(map[int]string, len(sessions))
	s.mediaPortCursor = start
	for _, session := range sessions {
		if session.Status == store.GB28181SessionStatusFailed || session.Status == store.GB28181SessionStatusTerminated {
			continue
		}
		_, mediaPort, _ := parseInviteSDPInfo(session.SDPBody)
		if mediaPort < start || mediaPort > end {
			continue
		}
		callID := strings.TrimSpace(session.CallID)
		if callID == "" {
			continue
		}
		if owner, exists := s.mediaCallByPort[mediaPort]; exists && owner != callID {
			continue
		}
		s.mediaPortByCall[callID] = mediaPort
		s.mediaCallByPort[mediaPort] = callID
	}
	if s.mediaPortCursor < start || s.mediaPortCursor > end {
		s.mediaPortCursor = start
	}
}

func (s *Service) Reinvite(ctx context.Context, callID string) (*store.GB28181Session, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("callId is required")
	}
	session, err := s.store.GetGB28181SessionByCallID(ctx, callID)
	if err != nil {
		return nil, err
	}
	mediaIP, _, ssrc := parseInviteSDPInfo(session.SDPBody)
	req := InviteRequest{
		DeviceID:  session.DeviceID,
		ChannelID: session.ChannelID,
		MediaIP:   mediaIP,
		MediaPort: 0, // Always use a fresh leased media port.
		SSRC:      ssrc,
		StreamID:  session.StreamID,
	}
	newSession, err := s.Invite(ctx, req)
	if err != nil {
		return nil, err
	}
	if session.Status != store.GB28181SessionStatusEstablished {
		s.releaseMediaPortByCallID(session.CallID)
	}
	return newSession, nil
}

func (s *Service) ExportSessionSDP(ctx context.Context, callID string) (*SessionSDPResult, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("callId is required")
	}
	session, err := s.store.GetGB28181SessionByCallID(ctx, callID)
	if err != nil {
		return nil, err
	}
	sdpBody := strings.TrimSpace(session.SDPBody)
	if sdpBody == "" {
		return nil, errors.New("session has no SDP offer body")
	}
	cfg := s.currentConfig()
	mediaIP, mediaPort, ssrc := parseInviteSDPInfo(sdpBody)
	if mediaIP == "" {
		mediaIP = strings.TrimSpace(cfg.GB28181ListenIP)
		if mediaIP == "" || mediaIP == "0.0.0.0" {
			mediaIP = "0.0.0.0"
		}
	}
	if mediaPort <= 0 {
		mediaPort = cfg.GB28181MediaPort
	}
	if mediaPort <= 0 {
		return nil, errors.New("invalid media port in session SDP")
	}
	if ssrc == "" {
		ssrc = generateNumericToken(10)
	}
	normalizedSDP := buildInviteSDP(mediaIP, mediaPort, ssrc, cfg.GB28181ServerID)
	sdpPath, err := s.writeSessionSDPFile(cfg, callID, normalizedSDP)
	if err != nil {
		return nil, err
	}
	return &SessionSDPResult{
		CallID:    session.CallID,
		DeviceID:  session.DeviceID,
		ChannelID: session.ChannelID,
		MediaIP:   mediaIP,
		MediaPort: mediaPort,
		SSRC:      ssrc,
		SDP:       normalizedSDP,
		SDPPath:   sdpPath,
	}, nil
}

func (s *Service) writeSessionSDPFile(cfg config.Config, callID string, sdp string) (string, error) {
	dir := filepath.Join(cfg.DataDir, "gb28181", "sdp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := sanitizeFileName(callID)
	if name == "" {
		name = "session"
	}
	target := filepath.Join(dir, name+".sdp")
	if err := os.WriteFile(target, []byte(sdp), 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func (s *Service) QueryCatalog(ctx context.Context, deviceID string) (map[string]any, error) {
	device, err := s.store.GetGB28181DeviceByDeviceID(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		return nil, err
	}
	callID := generateToken(20) + "@gb28181"
	sn := generateSN()
	body := fmt.Sprintf(`<?xml version="1.0"?>
<Query>
  <CmdType>Catalog</CmdType>
  <SN>%d</SN>
  <DeviceID>%s</DeviceID>
</Query>`, sn, xmlEscape(device.DeviceID))
	request := s.buildRequest("MESSAGE", fmt.Sprintf("sip:%s@%s", device.DeviceID, s.cfg.GB28181Realm), map[string]string{
		"Via":          s.buildVia(device.Transport),
		"From":         fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.GB28181ServerID, s.cfg.GB28181Realm, generateToken(8)),
		"To":           fmt.Sprintf("<sip:%s@%s>", device.DeviceID, s.cfg.GB28181Realm),
		"Call-ID":      callID,
		"CSeq":         "1 MESSAGE",
		"Max-Forwards": "70",
		"Content-Type": "Application/MANSCDP+xml",
		"User-Agent":   userAgent,
	}, body)
	if err := s.sendToDevice(ctx, device, request); err != nil {
		return nil, err
	}
	return map[string]any{
		"callId":   callID,
		"sn":       sn,
		"deviceId": device.DeviceID,
		"sent":     true,
		"request":  request,
	}, nil
}

func (s *Service) Invite(ctx context.Context, req InviteRequest) (*store.GB28181Session, error) {
	device, err := s.store.GetGB28181DeviceByDeviceID(ctx, strings.TrimSpace(req.DeviceID))
	if err != nil {
		return nil, err
	}
	channelID := strings.TrimSpace(req.ChannelID)
	if channelID == "" {
		return nil, errors.New("channelId is required")
	}
	mediaIP := strings.TrimSpace(req.MediaIP)
	if mediaIP == "" {
		mediaIP = strings.TrimSpace(s.cfg.GB28181MediaIP)
	}
	if mediaIP == "" {
		mediaIP = detectOutboundIP(device.RemoteAddr)
	}
	if mediaIP == "" {
		mediaIP = "127.0.0.1"
	}
	callID := generateToken(20) + "@gb28181"
	mediaPort := req.MediaPort
	if mediaPort > 0 {
		if err := s.reserveMediaPort(callID, mediaPort); err != nil {
			return nil, err
		}
	} else {
		allocatedPort, allocErr := s.allocateMediaPort(callID)
		if allocErr == nil {
			mediaPort = allocatedPort
		} else {
			mediaPort = s.cfg.GB28181MediaPort
			if mediaPort <= 0 {
				mediaPort = 30000
			}
			if err := s.reserveMediaPort(callID, mediaPort); err != nil {
				return nil, allocErr
			}
		}
	}
	ssrc := strings.TrimSpace(req.SSRC)
	if ssrc == "" {
		ssrc = generateNumericToken(10)
	}
	streamID := strings.TrimSpace(req.StreamID)
	if streamID == "" {
		streamID = req.ChannelID
	}
	branch := "z9hG4bK" + generateToken(10)
	sdp := buildInviteSDP(mediaIP, mediaPort, ssrc, s.cfg.GB28181ServerID)
	invite := s.buildRequest("INVITE", fmt.Sprintf("sip:%s@%s", channelID, s.cfg.GB28181Realm), map[string]string{
		"Via":          s.buildVia(device.Transport) + ";branch=" + branch,
		"From":         fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.GB28181ServerID, s.cfg.GB28181Realm, generateToken(8)),
		"To":           fmt.Sprintf("<sip:%s@%s>", channelID, s.cfg.GB28181Realm),
		"Call-ID":      callID,
		"CSeq":         "1 INVITE",
		"Contact":      fmt.Sprintf("<sip:%s@%s:%d>", s.cfg.GB28181ServerID, s.cfg.GB28181ListenIP, s.cfg.GB28181ListenPort),
		"Max-Forwards": "70",
		"Subject":      fmt.Sprintf("%s:%s,%s:0", channelID, ssrc, s.cfg.GB28181ServerID),
		"Content-Type": "Application/SDP",
		"User-Agent":   userAgent,
	}, sdp)
	if err := s.sendToDevice(ctx, device, invite); err != nil {
		s.releaseMediaPortByCallID(callID)
		return nil, err
	}
	session, err := s.store.UpsertGB28181Session(ctx, store.GB28181SessionUpsertRequest{
		DeviceID:   device.DeviceID,
		ChannelID:  channelID,
		CallID:     callID,
		Branch:     branch,
		StreamID:   streamID,
		RemoteAddr: device.RemoteAddr,
		Status:     store.GB28181SessionStatusInviting,
		SDPBody:    sdp,
	})
	if err != nil {
		s.releaseMediaPortByCallID(callID)
		return nil, err
	}
	return session, nil
}

func (s *Service) Bye(ctx context.Context, callID string) (*store.GB28181Session, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("callId is required")
	}
	session, err := s.store.GetGB28181SessionByCallID(ctx, callID)
	if err != nil {
		return nil, err
	}
	device, err := s.store.GetGB28181DeviceByDeviceID(ctx, session.DeviceID)
	if err != nil {
		return nil, err
	}
	request := s.buildRequest("BYE", fmt.Sprintf("sip:%s@%s", session.ChannelID, s.cfg.GB28181Realm), map[string]string{
		"Via":          s.buildVia(device.Transport) + ";branch=" + session.Branch,
		"From":         fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.GB28181ServerID, s.cfg.GB28181Realm, generateToken(8)),
		"To":           fmt.Sprintf("<sip:%s@%s>", session.ChannelID, s.cfg.GB28181Realm),
		"Call-ID":      session.CallID,
		"CSeq":         "2 BYE",
		"Max-Forwards": "70",
		"User-Agent":   userAgent,
	}, "")
	if err := s.sendToDevice(ctx, device, request); err != nil {
		return nil, err
	}
	updated, err := s.store.UpsertGB28181Session(ctx, store.GB28181SessionUpsertRequest{
		DeviceID:   session.DeviceID,
		ChannelID:  session.ChannelID,
		CallID:     session.CallID,
		Branch:     session.Branch,
		StreamID:   session.StreamID,
		RemoteAddr: session.RemoteAddr,
		Status:     store.GB28181SessionStatusTerminated,
	})
	if err != nil {
		return nil, err
	}
	s.releaseMediaPortByCallID(session.CallID)
	return updated, nil
}

func (s *Service) startUDP(cfg config.Config) error {
	listenIP := strings.TrimSpace(cfg.GB28181ListenIP)
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	addr := &net.UDPAddr{IP: net.ParseIP(listenIP), Port: cfg.GB28181ListenPort}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.udpConn = conn
	s.bindAddrs = append(s.bindAddrs, conn.LocalAddr().String())
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		buffer := make([]byte, 64*1024)
		for {
			n, remote, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				if !s.isRunning() {
					return
				}
				s.setLastError(readErr)
				continue
			}
			s.markReceive()
			raw := string(buffer[:n])
			if strings.TrimSpace(raw) == "" {
				continue
			}
			send := func(payload string) error {
				if strings.TrimSpace(payload) == "" {
					return nil
				}
				_, err := conn.WriteToUDP([]byte(payload), remote)
				if err == nil {
					s.markSent()
				}
				return err
			}
			s.handleMessage(context.Background(), raw, remote.String(), "udp", send)
		}
	}()
	return nil
}

func (s *Service) startTCP(cfg config.Config) error {
	listenIP := strings.TrimSpace(cfg.GB28181ListenIP)
	if listenIP == "" {
		listenIP = "0.0.0.0"
	}
	address := fmt.Sprintf("%s:%d", listenIP, cfg.GB28181ListenPort)
	ln, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.tcpListener = ln
	s.bindAddrs = append(s.bindAddrs, ln.Addr().String())
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				if !s.isRunning() {
					return
				}
				s.setLastError(acceptErr)
				continue
			}
			s.wg.Add(1)
			go func(c net.Conn) {
				defer s.wg.Done()
				s.handleTCPConnection(c)
			}(conn)
		}
	}()
	return nil
}
func (s *Service) handleTCPConnection(conn net.Conn) {
	defer conn.Close()
	peer := &tcpPeer{
		conn:      conn,
		remote:    conn.RemoteAddr().String(),
		lastSeen:  time.Now(),
		deviceIDs: make(map[string]struct{}),
	}
	s.registerTCPPeer(peer)
	defer s.unregisterTCPPeer(peer)

	reader := bufio.NewReader(conn)
	for {
		raw, readErr := readSIPPacket(reader)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return
			}
			s.setLastError(readErr)
			return
		}
		s.markReceive()
		send := func(payload string) error {
			if strings.TrimSpace(payload) == "" {
				return nil
			}
			peer.mu.Lock()
			defer peer.mu.Unlock()
			if _, err := io.WriteString(conn, payload); err != nil {
				return err
			}
			s.markSent()
			return nil
		}
		s.handleMessage(context.Background(), raw, peer.remote, "tcp", send)
	}
}

func (s *Service) handleMessage(ctx context.Context, raw string, remote string, transport string, send func(string) error) {
	msg, err := parseSIPMessage(raw)
	if err != nil {
		s.setLastError(err)
		return
	}
	if msg.IsResponse {
		s.handleResponse(ctx, msg)
		return
	}
	switch msg.Method {
	case "REGISTER":
		s.handleREGISTER(ctx, msg, remote, transport, send)
	case "MESSAGE":
		s.handleMESSAGE(ctx, msg, remote, transport, send)
	case "OPTIONS", "SUBSCRIBE", "NOTIFY", "BYE", "INVITE", "ACK":
		response := buildSIPResponse(msg, 200, "OK", map[string]string{"User-Agent": userAgent}, "")
		if err := send(response); err != nil {
			s.setLastError(err)
		}
	default:
		response := buildSIPResponse(msg, 405, "Method Not Allowed", map[string]string{"User-Agent": userAgent}, "")
		if err := send(response); err != nil {
			s.setLastError(err)
		}
	}
}

func (s *Service) handleREGISTER(ctx context.Context, msg *sipMessage, remote string, transport string, send func(string) error) {
	deviceID := firstNonEmpty(extractUser(msg.Header("From")), extractUser(msg.Header("To")), extractUser(msg.Header("Authorization")))
	if deviceID == "" {
		_ = send(buildSIPResponse(msg, 400, "Bad Request", map[string]string{"User-Agent": userAgent}, ""))
		return
	}

	devicePassword := strings.TrimSpace(s.cfg.GB28181Password)
	if current, err := s.store.GetGB28181DeviceByDeviceID(ctx, deviceID); err == nil && strings.TrimSpace(current.AuthPassword) != "" {
		devicePassword = strings.TrimSpace(current.AuthPassword)
	}

	if devicePassword != "" {
		auth := strings.TrimSpace(msg.Header("Authorization"))
		if auth == "" || !s.verifyAuthorization(deviceID, "REGISTER", msg.URI, auth, devicePassword) {
			nonce := s.issueNonce(deviceID)
			response := buildSIPResponse(msg, 401, "Unauthorized", map[string]string{
				"WWW-Authenticate": fmt.Sprintf(`Digest realm="%s",nonce="%s",algorithm=MD5,qop="auth"`, s.cfg.GB28181Realm, nonce),
				"User-Agent":       userAgent,
			}, "")
			_ = send(response)
			return
		}
	}

	expires := parseExpires(msg.Header("Expires"), msg.Header("Contact"), s.cfg.GB28181RegisterExpires)
	status := store.GB28181DeviceStatusOnline
	if expires == 0 {
		status = store.GB28181DeviceStatusOffline
	}
	now := time.Now().UTC()
	if _, err := s.store.UpsertGB28181RuntimeDevice(ctx, store.GB28181RuntimeDeviceUpsertRequest{
		DeviceID:       deviceID,
		Name:           deviceID,
		Transport:      transport,
		RemoteAddr:     remote,
		Expires:        expires,
		RawPayload:     msg.Raw,
		LastRegisterAt: &now,
		Status:         status,
	}); err != nil {
		s.setLastError(err)
	}

	if transport == "tcp" {
		s.bindDeviceToPeer(deviceID, remote)
	}
	response := buildSIPResponse(msg, 200, "OK", map[string]string{
		"Date":       time.Now().UTC().Format(time.RFC1123),
		"Expires":    strconv.Itoa(expires),
		"User-Agent": userAgent,
	}, "")
	if err := send(response); err != nil {
		s.setLastError(err)
		return
	}

	// Query catalog asynchronously after successful register to refresh channel list.
	if status == store.GB28181DeviceStatusOnline {
		go func(deviceID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if _, err := s.QueryCatalog(ctx, deviceID); err != nil {
				log.Printf("[gb28181][warn] auto catalog query failed for %s: %v", deviceID, err)
			}
		}(deviceID)
	}
}

func (s *Service) handleMESSAGE(ctx context.Context, msg *sipMessage, remote string, transport string, send func(string) error) {
	deviceID := firstNonEmpty(extractUser(msg.Header("From")), extractUser(msg.Header("To")))
	if deviceID == "" {
		_ = send(buildSIPResponse(msg, 400, "Bad Request", map[string]string{"User-Agent": userAgent}, ""))
		return
	}

	cmdType, parsedChannels := parseGBMessageBody(msg.Body)
	now := time.Now().UTC()
	switch strings.ToLower(cmdType) {
	case "keepalive":
		if err := s.store.TouchGB28181DeviceKeepalive(ctx, deviceID, remote, msg.Body); err != nil {
			_, _ = s.store.UpsertGB28181RuntimeDevice(ctx, store.GB28181RuntimeDeviceUpsertRequest{
				DeviceID:        deviceID,
				Name:            deviceID,
				Transport:       transport,
				RemoteAddr:      remote,
				RawPayload:      msg.Body,
				LastKeepaliveAt: &now,
				Status:          store.GB28181DeviceStatusOnline,
			})
		}
	case "catalog":
		_, _ = s.store.UpsertGB28181RuntimeDevice(ctx, store.GB28181RuntimeDeviceUpsertRequest{
			DeviceID:   deviceID,
			Name:       deviceID,
			Transport:  transport,
			RemoteAddr: remote,
			RawPayload: msg.Body,
			Status:     store.GB28181DeviceStatusOnline,
		})
		if len(parsedChannels) > 0 {
			if err := s.store.ReplaceGB28181ChannelsByDeviceID(ctx, deviceID, parsedChannels); err != nil {
				s.setLastError(err)
			}
		}
	default:
		_, _ = s.store.UpsertGB28181RuntimeDevice(ctx, store.GB28181RuntimeDeviceUpsertRequest{
			DeviceID:   deviceID,
			Name:       deviceID,
			Transport:  transport,
			RemoteAddr: remote,
			RawPayload: msg.Body,
			Status:     store.GB28181DeviceStatusOnline,
		})
	}

	if transport == "tcp" {
		s.bindDeviceToPeer(deviceID, remote)
	}
	response := buildSIPResponse(msg, 200, "OK", map[string]string{"User-Agent": userAgent}, "")
	if err := send(response); err != nil {
		s.setLastError(err)
	}
}

func (s *Service) handleResponse(ctx context.Context, msg *sipMessage) {
	_ = ctx
	if msg.StatusCode <= 0 {
		return
	}
	parts := strings.Fields(msg.Header("CSeq"))
	if len(parts) < 2 {
		return
	}
	method := strings.ToUpper(strings.TrimSpace(parts[1]))
	callID := strings.TrimSpace(msg.Header("Call-ID"))
	if callID == "" {
		return
	}
	session, _ := s.store.GetGB28181SessionByCallID(context.Background(), callID)
	deviceID := firstNonEmpty(extractUser(msg.Header("From")), extractUser(msg.Header("To")))
	channelID := ""
	branch := ""
	streamID := ""
	remoteAddr := ""
	if session != nil {
		if strings.TrimSpace(session.DeviceID) != "" {
			deviceID = strings.TrimSpace(session.DeviceID)
		}
		channelID = strings.TrimSpace(session.ChannelID)
		branch = strings.TrimSpace(session.Branch)
		streamID = strings.TrimSpace(session.StreamID)
		remoteAddr = strings.TrimSpace(session.RemoteAddr)
	}

	switch method {
	case "INVITE":
		// 2xx for INVITE requires ACK, otherwise some devices will stop sending media.
		if msg.StatusCode >= 200 && msg.StatusCode < 300 {
			if err := s.sendInviteACK(context.Background(), msg, session); err != nil {
				s.setLastError(err)
			}
		}
		nextStatus := store.GB28181SessionStatusEstablished
		if msg.StatusCode >= 300 {
			nextStatus = store.GB28181SessionStatusFailed
		}
		if deviceID != "" {
			sdpBody := ""
			if session == nil {
				sdpBody = msg.Body
			}
			_, _ = s.store.UpsertGB28181Session(context.Background(), store.GB28181SessionUpsertRequest{
				DeviceID:   deviceID,
				ChannelID:  channelID,
				CallID:     callID,
				Branch:     branch,
				StreamID:   streamID,
				RemoteAddr: remoteAddr,
				Status:     nextStatus,
				SDPBody:    sdpBody,
			})
		}
		if nextStatus == store.GB28181SessionStatusFailed {
			s.releaseMediaPortByCallID(callID)
		}
	case "BYE":
		if deviceID != "" {
			_, _ = s.store.UpsertGB28181Session(context.Background(), store.GB28181SessionUpsertRequest{
				DeviceID:   deviceID,
				ChannelID:  channelID,
				CallID:     callID,
				Branch:     branch,
				StreamID:   streamID,
				RemoteAddr: remoteAddr,
				Status:     store.GB28181SessionStatusTerminated,
			})
		}
		s.releaseMediaPortByCallID(callID)
	}
}

func (s *Service) sendInviteACK(ctx context.Context, response *sipMessage, session *store.GB28181Session) error {
	if response == nil || session == nil {
		return nil
	}
	device, err := s.store.GetGB28181DeviceByDeviceID(ctx, strings.TrimSpace(session.DeviceID))
	if err != nil {
		return err
	}
	seqNo := parseCSeqNumber(response.Header("CSeq"))
	if seqNo <= 0 {
		seqNo = 1
	}
	uri := strings.TrimSpace(response.URI)
	if uri == "" {
		uri = fmt.Sprintf("sip:%s@%s", session.ChannelID, s.cfg.GB28181Realm)
	}
	via := strings.TrimSpace(response.Header("Via"))
	if via == "" {
		via = s.buildVia(device.Transport) + ";branch=" + session.Branch
	}
	from := strings.TrimSpace(response.Header("From"))
	if from == "" {
		from = fmt.Sprintf("<sip:%s@%s>;tag=%s", s.cfg.GB28181ServerID, s.cfg.GB28181Realm, generateToken(8))
	}
	to := strings.TrimSpace(response.Header("To"))
	if to == "" {
		to = fmt.Sprintf("<sip:%s@%s>", session.ChannelID, s.cfg.GB28181Realm)
	}
	request := s.buildRequest("ACK", uri, map[string]string{
		"Via":          via,
		"From":         from,
		"To":           to,
		"Call-ID":      strings.TrimSpace(session.CallID),
		"CSeq":         fmt.Sprintf("%d ACK", seqNo),
		"Max-Forwards": "70",
		"User-Agent":   userAgent,
	}, "")
	return s.sendToDevice(ctx, device, request)
}

func (s *Service) sendToDevice(ctx context.Context, device *store.GB28181Device, payload string) error {
	_ = ctx
	if device == nil {
		return errors.New("device is required")
	}
	transport := normalizeTransportForDevice(device.Transport, s.cfg.GB28181Transport)
	switch transport {
	case "udp":
		return s.sendUDP(device.RemoteAddr, payload)
	case "tcp":
		return s.sendTCP(device.DeviceID, device.RemoteAddr, payload)
	default:
		return fmt.Errorf("unsupported transport: %s", transport)
	}
}

func (s *Service) sendUDP(remoteAddr string, payload string) error {
	s.mu.RLock()
	conn := s.udpConn
	cfg := s.cfg
	s.mu.RUnlock()
	if conn == nil {
		return errors.New("gb28181 udp listener is not running")
	}
	addr, err := net.ResolveUDPAddr("udp", normalizeRemoteAddr(strings.TrimSpace(remoteAddr), cfg.GB28181ListenPort))
	if err != nil {
		return err
	}
	if _, err := conn.WriteToUDP([]byte(payload), addr); err != nil {
		return err
	}
	s.markSent()
	return nil
}

func (s *Service) sendTCP(deviceID string, remoteAddr string, payload string) error {
	_ = remoteAddr
	peer := s.getTCPPeerByDeviceID(deviceID)
	if peer == nil {
		return errors.New("gb28181 tcp peer not connected; wait device to connect and register")
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	if _, err := io.WriteString(peer.conn, payload); err != nil {
		return err
	}
	peer.lastSeen = time.Now()
	s.markSent()
	return nil
}

func (s *Service) buildRequest(method string, uri string, headers map[string]string, body string) string {
	lines := make([]string, 0, len(headers)+8)
	lines = append(lines, fmt.Sprintf("%s %s %s", strings.ToUpper(strings.TrimSpace(method)), strings.TrimSpace(uri), defaultSipVersion))
	required := []string{"Via", "From", "To", "Call-ID", "CSeq"}
	for _, key := range required {
		if _, ok := headers[key]; !ok {
			headers[key] = ""
		}
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		lines = append(lines, key+": "+value)
	}
	lines = append(lines, "Content-Length: "+strconv.Itoa(len([]byte(body))))
	lines = append(lines, "", body)
	return strings.Join(lines, "\r\n")
}

func (s *Service) buildVia(transport string) string {
	proto := "UDP"
	if strings.EqualFold(strings.TrimSpace(transport), "tcp") {
		proto = "TCP"
	}
	host := strings.TrimSpace(s.cfg.GB28181ListenIP)
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("SIP/2.0/%s %s:%d", proto, host, s.cfg.GB28181ListenPort)
}
func (s *Service) issueNonce(deviceID string) string {
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	s.cleanupNonceLocked()
	value := generateToken(24)
	s.nonces[deviceID] = nonceEntry{Nonce: value, ExpiresAt: time.Now().Add(5 * time.Minute)}
	return value
}

func (s *Service) verifyAuthorization(deviceID string, method string, uri string, header string, password string) bool {
	params := parseDigestAuthorization(header)
	if len(params) == 0 {
		return false
	}
	nonce := strings.TrimSpace(params["nonce"])
	if nonce == "" {
		return false
	}
	s.nonceMu.Lock()
	entry, ok := s.nonces[deviceID]
	s.nonceMu.Unlock()
	if !ok || entry.Nonce != nonce || time.Now().After(entry.ExpiresAt) {
		return false
	}
	username := strings.TrimSpace(params["username"])
	if username == "" {
		username = deviceID
	}
	realm := strings.TrimSpace(params["realm"])
	if realm == "" {
		realm = s.cfg.GB28181Realm
	}
	response := strings.ToLower(strings.TrimSpace(params["response"]))
	if response == "" {
		return false
	}
	ha1 := md5Hex(username + ":" + realm + ":" + password)
	ha2 := md5Hex(strings.ToUpper(strings.TrimSpace(method)) + ":" + strings.TrimSpace(uri))
	qop := strings.TrimSpace(params["qop"])
	expected := ""
	if qop != "" {
		expected = md5Hex(ha1 + ":" + nonce + ":" + strings.TrimSpace(params["nc"]) + ":" + strings.TrimSpace(params["cnonce"]) + ":" + qop + ":" + ha2)
	} else {
		expected = md5Hex(ha1 + ":" + nonce + ":" + ha2)
	}
	return strings.EqualFold(expected, response)
}

func (s *Service) cleanupNonceLocked() {
	now := time.Now()
	for key, entry := range s.nonces {
		if now.After(entry.ExpiresAt) {
			delete(s.nonces, key)
		}
	}
}

func (s *Service) markReceive() {
	atomic.AddInt64(&s.received, 1)
	now := time.Now().UTC()
	s.mu.Lock()
	s.lastPacket = now
	s.mu.Unlock()
}

func (s *Service) markSent() {
	atomic.AddInt64(&s.sent, 1)
}

func (s *Service) setLastError(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	s.lastError = err.Error()
	s.mu.Unlock()
	log.Printf("[gb28181][error] %v", err)
}

func (s *Service) isRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Service) registerTCPPeer(peer *tcpPeer) {
	s.tcpPeersMu.Lock()
	defer s.tcpPeersMu.Unlock()
	s.tcpPeers[peer.remote] = peer
}

func (s *Service) unregisterTCPPeer(peer *tcpPeer) {
	s.tcpPeersMu.Lock()
	defer s.tcpPeersMu.Unlock()
	delete(s.tcpPeers, peer.remote)
}

func (s *Service) closeAllTCPPeers() {
	s.tcpPeersMu.Lock()
	defer s.tcpPeersMu.Unlock()
	for _, peer := range s.tcpPeers {
		_ = peer.conn.Close()
	}
	s.tcpPeers = make(map[string]*tcpPeer)
}

func (s *Service) bindDeviceToPeer(deviceID string, remote string) {
	deviceID = strings.TrimSpace(deviceID)
	remote = strings.TrimSpace(remote)
	if deviceID == "" || remote == "" {
		return
	}
	s.tcpPeersMu.Lock()
	defer s.tcpPeersMu.Unlock()
	if peer, ok := s.tcpPeers[remote]; ok {
		peer.deviceIDs[deviceID] = struct{}{}
	}
}

func (s *Service) getTCPPeerByDeviceID(deviceID string) *tcpPeer {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil
	}
	s.tcpPeersMu.RLock()
	defer s.tcpPeersMu.RUnlock()
	for _, peer := range s.tcpPeers {
		if _, ok := peer.deviceIDs[deviceID]; ok {
			return peer
		}
	}
	return nil
}

type sipMessage struct {
	Raw        string
	StartLine  string
	Method     string
	URI        string
	IsResponse bool
	StatusCode int
	Reason     string
	Headers    map[string][]string
	Body       string
}

func (m *sipMessage) Header(name string) string {
	if m == nil {
		return ""
	}
	values := m.Headers[strings.ToLower(strings.TrimSpace(name))]
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func parseSIPMessage(raw string) (*sipMessage, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty sip message")
	}
	headerPart := raw
	bodyPart := ""
	if idx := strings.Index(raw, "\r\n\r\n"); idx >= 0 {
		headerPart = raw[:idx]
		bodyPart = raw[idx+4:]
	} else if idx := strings.Index(raw, "\n\n"); idx >= 0 {
		headerPart = raw[:idx]
		bodyPart = raw[idx+2:]
	}
	lines := splitLines(headerPart)
	if len(lines) == 0 {
		return nil, errors.New("invalid sip message")
	}
	msg := &sipMessage{
		Raw:       raw,
		StartLine: strings.TrimSpace(lines[0]),
		Headers:   make(map[string][]string),
		Body:      strings.TrimSpace(bodyPart),
	}
	first := strings.TrimSpace(lines[0])
	if strings.HasPrefix(strings.ToUpper(first), defaultSipVersion) {
		msg.IsResponse = true
		parts := strings.Fields(first)
		if len(parts) >= 2 {
			msg.StatusCode, _ = strconv.Atoi(parts[1])
		}
		if len(parts) >= 3 {
			msg.Reason = strings.Join(parts[2:], " ")
		}
	} else {
		parts := strings.Fields(first)
		if len(parts) < 3 {
			return nil, errors.New("invalid sip request line")
		}
		msg.Method = strings.ToUpper(strings.TrimSpace(parts[0]))
		msg.URI = strings.TrimSpace(parts[1])
	}
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		value := strings.TrimSpace(line[idx+1:])
		msg.Headers[key] = append(msg.Headers[key], value)
	}
	if msg.URI == "" {
		if to := msg.Header("To"); to != "" {
			if idx := strings.Index(strings.ToLower(to), "sip:"); idx >= 0 {
				msg.URI = strings.TrimSpace(to[idx:])
				if end := strings.IndexAny(msg.URI, ">;"); end > 0 {
					msg.URI = msg.URI[:end]
				}
			}
		}
	}
	return msg, nil
}

func splitLines(raw string) []string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	raw = strings.ReplaceAll(raw, "\r", "\n")
	chunks := strings.Split(raw, "\n")
	lines := make([]string, 0, len(chunks))
	for _, line := range chunks {
		lines = append(lines, strings.TrimRight(line, "\x00"))
	}
	return lines
}
func readSIPPacket(reader *bufio.Reader) (string, error) {
	headers := make([]string, 0, 24)
	contentLength := 0
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}
		trimmed := strings.TrimRight(line, "\r\n")
		headers = append(headers, trimmed)
		if strings.EqualFold(strings.TrimSpace(trimmed), "") {
			break
		}
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			value := strings.TrimSpace(trimmed[len("content-length:"):])
			contentLength, _ = strconv.Atoi(value)
			if contentLength < 0 {
				contentLength = 0
			}
		}
	}
	body := ""
	if contentLength > 0 {
		buffer := make([]byte, contentLength)
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return "", err
		}
		body = string(buffer)
	}
	payload := strings.Join(headers, "\r\n") + body
	return payload, nil
}

func buildSIPResponse(req *sipMessage, statusCode int, reason string, extraHeaders map[string]string, body string) string {
	lines := make([]string, 0, 20)
	lines = append(lines, fmt.Sprintf("%s %d %s", defaultSipVersion, statusCode, reason))
	appendHeader := func(key string, value string) {
		if strings.TrimSpace(value) == "" {
			return
		}
		lines = append(lines, key+": "+value)
	}
	appendHeader("Via", req.Header("Via"))
	appendHeader("From", req.Header("From"))
	appendHeader("To", ensureTag(req.Header("To")))
	appendHeader("Call-ID", req.Header("Call-ID"))
	appendHeader("CSeq", req.Header("CSeq"))
	for key, value := range extraHeaders {
		appendHeader(key, value)
	}
	appendHeader("Content-Length", strconv.Itoa(len([]byte(body))))
	lines = append(lines, "", body)
	return strings.Join(lines, "\r\n")
}

func ensureTag(toHeader string) string {
	toHeader = strings.TrimSpace(toHeader)
	if toHeader == "" {
		return toHeader
	}
	if strings.Contains(strings.ToLower(toHeader), ";tag=") {
		return toHeader
	}
	return toHeader + ";tag=" + generateToken(8)
}

func extractUser(header string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	lower := strings.ToLower(header)
	idx := strings.Index(lower, "sip:")
	if idx < 0 {
		return ""
	}
	value := header[idx+4:]
	end := len(value)
	for _, sep := range []string{"@", ";", ">", " "} {
		if p := strings.Index(value, sep); p >= 0 && p < end {
			end = p
		}
	}
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(value[:end])
}

func parseExpires(header string, contact string, fallback int) int {
	if fallback <= 0 {
		fallback = 3600
	}
	if value, err := strconv.Atoi(strings.TrimSpace(header)); err == nil {
		if value >= 0 {
			return value
		}
	}
	lower := strings.ToLower(contact)
	idx := strings.Index(lower, "expires=")
	if idx >= 0 {
		raw := contact[idx+len("expires="):]
		end := len(raw)
		for _, sep := range []string{";", ">", ","} {
			if p := strings.Index(raw, sep); p >= 0 && p < end {
				end = p
			}
		}
		if value, err := strconv.Atoi(strings.TrimSpace(raw[:end])); err == nil && value >= 0 {
			return value
		}
	}
	return fallback
}

func parseCSeqNumber(raw string) int {
	parts := strings.Fields(strings.TrimSpace(raw))
	if len(parts) == 0 {
		return 0
	}
	value, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0
	}
	return value
}

func parseInviteSDPInfo(sdp string) (mediaIP string, mediaPort int, ssrc string) {
	lines := splitLines(strings.TrimSpace(sdp))
	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "c=") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				mediaIP = strings.TrimSpace(fields[2])
			}
			continue
		}
		if strings.HasPrefix(lower, "m=video ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				mediaPort, _ = strconv.Atoi(strings.TrimSpace(fields[1]))
			}
			continue
		}
		if strings.HasPrefix(lower, "y=") {
			ssrc = strings.TrimSpace(strings.TrimPrefix(line, "y="))
		}
	}
	return mediaIP, mediaPort, ssrc
}

func parseDigestAuthorization(header string) map[string]string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	result := make(map[string]string)
	lower := strings.ToLower(header)
	if strings.HasPrefix(lower, "digest ") {
		header = strings.TrimSpace(header[len("digest "):])
	}
	parts := strings.Split(header, ",")
	for _, part := range parts {
		token := strings.TrimSpace(part)
		if token == "" {
			continue
		}
		idx := strings.Index(token, "=")
		if idx <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(token[:idx]))
		value := strings.Trim(strings.TrimSpace(token[idx+1:]), `"`)
		result[key] = value
	}
	return result
}

func parseGBMessageBody(body string) (string, []store.GB28181ChannelUpsertRequest) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", nil
	}
	type quick struct {
		CmdType  string `xml:"CmdType"`
		DeviceID string `xml:"DeviceID"`
	}
	var q quick
	_ = xml.Unmarshal([]byte(body), &q)
	cmd := strings.TrimSpace(q.CmdType)
	if !strings.EqualFold(cmd, "catalog") {
		return cmd, nil
	}
	type catalogItem struct {
		DeviceID     string `xml:"DeviceID"`
		Name         string `xml:"Name"`
		Manufacturer string `xml:"Manufacturer"`
		Model        string `xml:"Model"`
		Owner        string `xml:"Owner"`
		CivilCode    string `xml:"CivilCode"`
		Address      string `xml:"Address"`
		Parental     int    `xml:"Parental"`
		ParentID     string `xml:"ParentID"`
		SafetyWay    int    `xml:"SafetyWay"`
		RegisterWay  int    `xml:"RegisterWay"`
		Secrecy      int    `xml:"Secrecy"`
		Status       string `xml:"Status"`
		Longitude    string `xml:"Longitude"`
		Latitude     string `xml:"Latitude"`
	}
	type catalogPayload struct {
		CmdType    string `xml:"CmdType"`
		DeviceID   string `xml:"DeviceID"`
		DeviceList struct {
			Items []catalogItem `xml:"Item"`
		} `xml:"DeviceList"`
	}
	var payload catalogPayload
	if err := xml.Unmarshal([]byte(body), &payload); err != nil {
		return cmd, nil
	}
	channels := make([]store.GB28181ChannelUpsertRequest, 0, len(payload.DeviceList.Items))
	for _, item := range payload.DeviceList.Items {
		channelID := strings.TrimSpace(item.DeviceID)
		if channelID == "" {
			continue
		}
		channels = append(channels, store.GB28181ChannelUpsertRequest{
			DeviceID:     strings.TrimSpace(payload.DeviceID),
			ChannelID:    channelID,
			Name:         strings.TrimSpace(item.Name),
			Manufacturer: strings.TrimSpace(item.Manufacturer),
			Model:        strings.TrimSpace(item.Model),
			Owner:        strings.TrimSpace(item.Owner),
			CivilCode:    strings.TrimSpace(item.CivilCode),
			Address:      strings.TrimSpace(item.Address),
			Parental:     item.Parental,
			ParentID:     strings.TrimSpace(item.ParentID),
			SafetyWay:    item.SafetyWay,
			RegisterWay:  item.RegisterWay,
			Secrecy:      item.Secrecy,
			Status:       strings.TrimSpace(item.Status),
			Longitude:    strings.TrimSpace(item.Longitude),
			Latitude:     strings.TrimSpace(item.Latitude),
			RawPayload:   body,
		})
	}
	return cmd, channels
}

func buildInviteSDP(mediaIP string, mediaPort int, ssrc string, owner string) string {
	return strings.Join([]string{
		"v=0",
		fmt.Sprintf("o=%s 0 0 IN IP4 %s", owner, mediaIP),
		"s=Play",
		fmt.Sprintf("c=IN IP4 %s", mediaIP),
		"t=0 0",
		fmt.Sprintf("m=video %d RTP/AVP 96 98 97", mediaPort),
		"a=recvonly",
		"a=rtpmap:96 PS/90000",
		"a=rtpmap:98 H264/90000",
		"a=rtpmap:97 MPEG4/90000",
		fmt.Sprintf("y=%s", ssrc),
		"",
	}, "\r\n")
}
func generateSN() int64 {
	return time.Now().UnixNano() % 100000000000
}

func generateToken(bytesLen int) string {
	if bytesLen <= 0 {
		bytesLen = 16
	}
	bytes := make([]byte, bytesLen)
	if _, err := rand.Read(bytes); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(bytes)
}

func generateNumericToken(length int) string {
	if length <= 0 {
		length = 10
	}
	raw := generateToken(length)
	digits := make([]byte, 0, length)
	for i := 0; i < len(raw) && len(digits) < length; i++ {
		ch := raw[i]
		if ch >= '0' && ch <= '9' {
			digits = append(digits, ch)
			continue
		}
		if ch >= 'a' && ch <= 'f' {
			digits = append(digits, byte('0'+(ch-'a')%10))
		}
	}
	for len(digits) < length {
		digits = append(digits, '0')
	}
	return string(digits)
}

func md5Hex(value string) string {
	sum := md5.Sum([]byte(value))
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func xmlEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(value)
}

func gbConfigEqual(a, b config.Config) bool {
	return a.GB28181Enabled == b.GB28181Enabled &&
		strings.EqualFold(strings.TrimSpace(a.GB28181ListenIP), strings.TrimSpace(b.GB28181ListenIP)) &&
		a.GB28181ListenPort == b.GB28181ListenPort &&
		strings.EqualFold(strings.TrimSpace(a.GB28181Transport), strings.TrimSpace(b.GB28181Transport)) &&
		strings.TrimSpace(a.GB28181ServerID) == strings.TrimSpace(b.GB28181ServerID) &&
		strings.TrimSpace(a.GB28181Realm) == strings.TrimSpace(b.GB28181Realm) &&
		strings.TrimSpace(a.GB28181Password) == strings.TrimSpace(b.GB28181Password) &&
		a.GB28181RegisterExpires == b.GB28181RegisterExpires &&
		a.GB28181HeartbeatInterval == b.GB28181HeartbeatInterval &&
		strings.TrimSpace(a.GB28181MediaIP) == strings.TrimSpace(b.GB28181MediaIP) &&
		a.GB28181MediaPort == b.GB28181MediaPort
}

func normalizeTransportForDevice(deviceTransport string, defaultTransport string) string {
	deviceTransport = strings.ToLower(strings.TrimSpace(deviceTransport))
	switch deviceTransport {
	case "udp", "tcp":
		return deviceTransport
	}
	defaultTransport = strings.ToLower(strings.TrimSpace(defaultTransport))
	switch defaultTransport {
	case "udp", "tcp":
		return defaultTransport
	case "both":
		return "udp"
	default:
		return "udp"
	}
}

func normalizeRemoteAddr(addr string, defaultPort int) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return addr
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	if defaultPort <= 0 {
		defaultPort = 5060
	}
	return net.JoinHostPort(addr, strconv.Itoa(defaultPort))
}

func sanitizeFileName(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, ch := range value {
		if unicode.IsLetter(ch) || unicode.IsDigit(ch) || ch == '-' || ch == '_' || ch == '.' {
			b.WriteRune(ch)
			continue
		}
		b.WriteByte('_')
	}
	result := strings.Trim(b.String(), "._")
	if result == "" {
		return ""
	}
	if len(result) > 128 {
		result = result[:128]
	}
	return result
}

func normalizeMediaPortRange(cfg config.Config) (int, int) {
	start := cfg.GB28181MediaPortStart
	end := cfg.GB28181MediaPortEnd
	if start <= 0 {
		start = cfg.GB28181MediaPort
	}
	if start <= 0 {
		start = 30000
	}
	if end <= 0 {
		end = start + 100
	}
	if end < start {
		start, end = end, start
	}
	// Keep pool reasonably bounded to avoid accidental huge scans.
	if end-start > 4000 {
		end = start + 4000
	}
	return start, end
}

func sortInts(values []int) {
	sort.Ints(values)
}

func detectOutboundIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil || host == "" {
		return ""
	}
	conn, err := net.DialTimeout("udp", net.JoinHostPort(host, "9"), 2*time.Second)
	if err != nil {
		return ""
	}
	defer conn.Close()
	local := conn.LocalAddr()
	if local == nil {
		return ""
	}
	localHost, _, err := net.SplitHostPort(local.String())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(localHost)
}
