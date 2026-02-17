package webrtcpreview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type Service struct {
	mu          sync.Mutex
	sessions    map[string]*previewSession
	maxSessions int
	debug       bool
}

type previewSession struct {
	id        string
	sourceURL string
	createdAt time.Time

	client *gortsplib.Client
	peer   *webrtc.PeerConnection

	closeOnce sync.Once
}

type OfferAnswer struct {
	SessionID string `json:"sessionId"`
	Type      string `json:"type"`
	SDP       string `json:"sdp"`
}

func New(maxSessions int, debug bool) *Service {
	if maxSessions <= 0 {
		maxSessions = 16
	}
	if maxSessions > 128 {
		maxSessions = 128
	}
	return &Service{
		sessions:    make(map[string]*previewSession),
		maxSessions: maxSessions,
		debug:       debug,
	}
}

func (s *Service) UpdateDebug(enabled bool) {
	s.mu.Lock()
	s.debug = enabled
	s.mu.Unlock()
}

func (s *Service) StartRTSPPreview(ctx context.Context, sourceURL string, offerSDP string) (*OfferAnswer, error) {
	sourceURL = strings.TrimSpace(sourceURL)
	offerSDP = strings.TrimSpace(offerSDP)
	if sourceURL == "" {
		return nil, errors.New("webrtc preview source url is required")
	}
	if offerSDP == "" {
		return nil, errors.New("webrtc offer sdp is required")
	}

	rtspURL, err := base.ParseURL(sourceURL)
	if err != nil {
		return nil, fmt.Errorf("parse rtsp url failed: %w", err)
	}
	if rtspURL == nil || (rtspURL.Scheme != "rtsp" && rtspURL.Scheme != "rtsps") {
		return nil, errors.New("webrtc preview currently supports rtsp / rtsps source")
	}

	client := &gortsplib.Client{
		Scheme:       rtspURL.Scheme,
		Host:         rtspURL.Host,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		UserAgent:    "gover-webrtc-preview/1.0",
	}

	if err := client.Start(); err != nil {
		return nil, fmt.Errorf("connect rtsp server failed: %w", err)
	}

	desc, _, err := client.Describe(rtspURL)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("rtsp describe failed: %w", err)
	}

	var h264Format *format.H264
	media := desc.FindFormat(&h264Format)
	if media == nil {
		client.Close()
		return nil, errors.New("rtsp source has no H264 video track; WebRTC preview currently supports H264 only")
	}

	if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
		client.Close()
		return nil, fmt.Errorf("rtsp setup failed: %w", err)
	}

	mediaEngine := &webrtc.MediaEngine{}
	if err := mediaEngine.RegisterDefaultCodecs(); err != nil {
		client.Close()
		return nil, fmt.Errorf("register webrtc codecs failed: %w", err)
	}

	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine))
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("create webrtc peer connection failed: %w", err)
	}

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000},
		"video",
		"gover-preview",
	)
	if err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("create webrtc video track failed: %w", err)
	}

	sender, err := peer.AddTrack(videoTrack)
	if err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("add webrtc track failed: %w", err)
	}

	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(buf); readErr != nil {
				return
			}
		}
	}()

	payloadType := uint8(96)
	if params := sender.GetParameters(); len(params.Codecs) > 0 {
		payloadType = uint8(params.Codecs[0].PayloadType)
	}

	client.OnPacketRTP(media, h264Format, func(pkt *rtp.Packet) {
		if pkt == nil {
			return
		}
		packet := *pkt
		packet.PayloadType = payloadType
		if writeErr := videoTrack.WriteRTP(&packet); writeErr != nil {
			if s.debug && !errors.Is(writeErr, io.ErrClosedPipe) {
				log.Printf("[webrtc-preview] write rtp failed: %v", writeErr)
			}
		}
	})

	offer := webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}
	if err := peer.SetRemoteDescription(offer); err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("set remote description failed: %w", err)
	}

	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("create webrtc answer failed: %w", err)
	}

	gatherDone := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(answer); err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("set local description failed: %w", err)
	}

	select {
	case <-gatherDone:
	case <-ctx.Done():
		_ = peer.Close()
		client.Close()
		return nil, ctx.Err()
	case <-time.After(6 * time.Second):
		if s.debug {
			log.Printf("[webrtc-preview] ice gathering timeout, continue with partial candidates")
		}
	}

	if _, err := client.Play(nil); err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("rtsp play failed: %w", err)
	}

	sessionID, err := randomSessionID(12)
	if err != nil {
		_ = peer.Close()
		client.Close()
		return nil, fmt.Errorf("generate session id failed: %w", err)
	}

	sess := &previewSession{
		id:        sessionID,
		sourceURL: sourceURL,
		createdAt: time.Now().UTC(),
		client:    client,
		peer:      peer,
	}
	s.registerSession(sess)

	peer.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if s.debug {
			log.Printf("[webrtc-preview][%s] connection state: %s", sessionID, state.String())
		}
		switch state {
		case webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateClosed:
			s.CloseSession(sessionID)
		}
	})

	go func(id string, c *gortsplib.Client) {
		err := c.Wait()
		if err != nil && s.debug {
			log.Printf("[webrtc-preview][%s] rtsp wait ended: %v", id, err)
		}
		s.CloseSession(id)
	}(sessionID, client)

	localDesc := peer.LocalDescription()
	if localDesc == nil {
		s.CloseSession(sessionID)
		return nil, errors.New("local webrtc description is empty")
	}

	return &OfferAnswer{
		SessionID: sessionID,
		Type:      localDesc.Type.String(),
		SDP:       localDesc.SDP,
	}, nil
}

func (s *Service) CloseSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	sess := s.sessions[sessionID]
	if sess != nil {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if sess == nil {
		return
	}
	sess.close()
}

func (s *Service) CloseAll() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.CloseSession(id)
	}
}

func (s *Service) registerSession(sess *previewSession) {
	s.mu.Lock()
	s.sessions[sess.id] = sess

	overflow := len(s.sessions) - s.maxSessions
	toClose := make([]*previewSession, 0)
	if overflow > 0 {
		list := make([]*previewSession, 0, len(s.sessions))
		for _, item := range s.sessions {
			list = append(list, item)
		}
		sort.Slice(list, func(i, j int) bool {
			return list[i].createdAt.Before(list[j].createdAt)
		})
		for i := 0; i < overflow && i < len(list); i++ {
			victim := list[i]
			if victim == nil || victim.id == sess.id {
				continue
			}
			if _, ok := s.sessions[victim.id]; !ok {
				continue
			}
			delete(s.sessions, victim.id)
			toClose = append(toClose, victim)
		}
	}
	s.mu.Unlock()

	for _, victim := range toClose {
		if s.debug {
			log.Printf("[webrtc-preview] close old session due to limit: %s", victim.id)
		}
		victim.close()
	}
}

func (s *previewSession) close() {
	s.closeOnce.Do(func() {
		if s.peer != nil {
			_ = s.peer.Close()
		}
		if s.client != nil {
			s.client.Close()
		}
	})
}

func randomSessionID(bytesSize int) (string, error) {
	if bytesSize <= 0 {
		bytesSize = 8
	}
	buffer := make([]byte, bytesSize)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
