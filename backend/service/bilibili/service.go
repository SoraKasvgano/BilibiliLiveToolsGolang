package bilibili

import (
	"bytes"
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skip2/go-qrcode"

	"bilibililivetools/gover/backend/config"
	"bilibililivetools/gover/backend/store"
)

const (
	navAPI                = "https://api.bilibili.com/x/web-interface/nav"
	qrCodeGenerateAPI     = "https://passport.bilibili.com/x/passport-login/web/qrcode/generate"
	qrCodePollAPI         = "https://passport.bilibili.com/x/passport-login/web/qrcode/poll"
	getMyLiveRoomInfoAPI  = "https://api.live.bilibili.com/xlive/app-blink/v1/room/GetInfo?platform=pc"
	getRoomInfoOldAPI     = "https://api.live.bilibili.com/room/v1/Room/getRoomInfoOld"
	getRoomInfoAPI        = "https://api.live.bilibili.com/room/v1/Room/get_info"
	getLiveAreaAPI        = "https://api.live.bilibili.com/room/v1/Area/getList"
	updateLiveRoomInfoAPI = "https://api.live.bilibili.com/room/v1/Room/update"
	updateRoomNewsAPI     = "https://api.live.bilibili.com/xlive/app-blink/v1/index/updateRoomNews"
	startLiveAPI          = "https://api.live.bilibili.com/room/v1/Room/startLive"
	stopLiveAPI           = "https://api.live.bilibili.com/room/v1/Room/stopLive"
	sendDanmakuAPI        = "https://api.live.bilibili.com/msg/send"
	sendDanmakuAPIAlt     = "https://api.live.bilibili.com/xlive/web-room/v1/dM/sendMsg"
	liveVersionAPI        = "https://api.live.bilibili.com/xlive/app-blink/v1/liveVersionInfo/getHomePageLiveVersion"
	cookieInfoAPI         = "https://passport.bilibili.com/x/passport-login/web/cookie/info"
	getRefreshCsrfAPI     = "https://www.bilibili.com/correspond/1/%s"
	refreshCookieAPI      = "https://passport.bilibili.com/x/passport-login/web/cookie/refresh"
	confirmRefreshAPI     = "https://passport.bilibili.com/x/passport-login/web/confirm/refresh"

	refreshPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDLgd2OAkcGVtoE3ThUREbio0Eg
Uc/prcajMKXvkCKFCWhJYJcLkcM2DKKcSeFpD/j6Boy538YXnR6VhcuUJOhH2x71
nzPjfdTcqMz7djHum0qSZA0AyCBDABUqCrfNgCiJ00Ra7GmRj+YCK1NJEuewlb40
JNrRuoEUXpabUzGB8QIDAQAB
-----END PUBLIC KEY-----`
)

type Service interface {
	GetLoginStatus(ctx context.Context) (store.LoginStatus, error)
	RequestQRCodeLogin(ctx context.Context) (*store.QrCodeStatus, error)
	SetCookie(ctx context.Context, content string) error
	Logout(ctx context.Context) error
	GetStreamURL(ctx context.Context, live *store.LiveSetting) (string, error)
	GetMyLiveRoomInfo(ctx context.Context) (*store.MyLiveRoomInfo, error)
	GetLiveAreas(ctx context.Context) ([]store.LiveAreaItem, error)
	UpdateLiveRoomInfo(ctx context.Context, roomID int64, title string, areaID int) error
	UpdateRoomNews(ctx context.Context, roomID int64, content string) error
	StopLive(ctx context.Context, roomID int64) error
	SendDanmaku(ctx context.Context, roomID int64, message string) (map[string]any, error)
	CookieNeedToRefresh(ctx context.Context) (bool, error)
	RefreshCookie(ctx context.Context) error
	SetManualStreamURL(url string)
}

type APIService struct {
	store  *store.Store
	cfg    config.Config
	client *http.Client

	mu              sync.RWMutex
	manualStreamURL string
	qrState         *qrState
	areas           []store.LiveAreaItem
	areasExpireAt   time.Time
}

func (s *APIService) logInfo(format string, args ...any) {
	log.Printf("[bilibili] "+format, args...)
}

func (s *APIService) logWarn(format string, args ...any) {
	log.Printf("[bilibili][warn] "+format, args...)
}

func (s *APIService) logError(format string, args ...any) {
	log.Printf("[bilibili][error] "+format, args...)
}

type apiErrorReport struct {
	Endpoint        string
	Method          string
	Stage           string
	HTTPStatus      int
	Attempt         int
	Retryable       bool
	RequestForm     string
	ResponseHeaders string
	ResponseBody    string
	Detail          string
}

type bilibiliAPIError struct {
	report apiErrorReport
}

func (e *bilibiliAPIError) Error() string {
	return e.report.Detail
}

func (s *APIService) recordAPIError(report apiErrorReport) {
	report.Endpoint = strings.TrimSpace(report.Endpoint)
	report.Method = strings.ToUpper(strings.TrimSpace(report.Method))
	report.Stage = strings.TrimSpace(report.Stage)
	report.Detail = strings.TrimSpace(report.Detail)
	if report.Stage == "" {
		report.Stage = "unknown"
	}
	if report.Method == "" {
		report.Method = "UNKNOWN"
	}

	logID, insertErr := s.store.CreateBilibiliAPIErrorLog(context.Background(), store.BilibiliAPIErrorLog{
		Endpoint:        report.Endpoint,
		Method:          report.Method,
		Stage:           report.Stage,
		HTTPStatus:      report.HTTPStatus,
		Attempt:         report.Attempt,
		Retryable:       report.Retryable,
		RequestForm:     report.RequestForm,
		ResponseHeaders: report.ResponseHeaders,
		ResponseBody:    report.ResponseBody,
		ErrorMessage:    report.Detail,
	})
	if insertErr != nil {
		s.logError("record bilibili_api_error_logs failed: %v", insertErr)
	}

	body, marshalErr := json.Marshal(map[string]any{
		"endpoint":     report.Endpoint,
		"method":       report.Method,
		"stage":        report.Stage,
		"httpStatus":   report.HTTPStatus,
		"detail":       report.Detail,
		"attempt":      report.Attempt,
		"retryable":    report.Retryable,
		"errorLogId":   logID,
		"responseSize": len(report.ResponseBody),
		"time":         time.Now().Format(time.RFC3339Nano),
	})
	if marshalErr != nil {
		s.logError("marshal api error payload failed: %v", marshalErr)
		return
	}
	if err := s.store.CreateLiveEvent(context.Background(), "bilibili.api.error", string(body)); err != nil {
		s.logError("record api error event failed: %v", err)
	}
}

type qrState struct {
	status      store.QrCodeStatus
	expireAt    time.Time
	lastPollAt  time.Time
	isCompleted bool
}

func New(storeDB *store.Store, cfg config.Config) *APIService {
	return &APIService{
		store: storeDB,
		cfg:   cfg,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
		manualStreamURL: strings.TrimSpace(""),
	}
}

func (s *APIService) readConfig() config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *APIService) UpdateConfig(cfg config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

func (s *APIService) SetManualStreamURL(streamURL string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.manualStreamURL = strings.TrimSpace(streamURL)
}

func (s *APIService) GetLoginStatus(ctx context.Context) (store.LoginStatus, error) {
	status := store.LoginStatus{Status: store.AccountStatusNotLogin}
	cookieSetting, err := s.store.GetCookieSetting(ctx)
	if err != nil {
		return status, err
	}

	if qr := s.getQRCodeStatus(); qr != nil {
		status.Status = store.AccountStatusLogging
		status.QrCodeStatus = qr
	}

	if strings.TrimSpace(cookieSetting.Content) == "" {
		if status.QrCodeStatus == nil {
			status.Message = "Not logged in"
		}
		return status, nil
	}

	if need, err := s.CookieNeedToRefresh(ctx); err == nil && need {
		if refreshErr := s.RefreshCookie(ctx); refreshErr != nil {
			s.logWarn("cookie refresh failed in status check: %v", refreshErr)
			status.Message = "Cookie refresh failed: " + refreshErr.Error()
		}
	}

	user, err := s.getUserInfo(ctx)
	if err != nil {
		s.logWarn("get user info failed in status: %v", err)
		status.Message = "Cookie exists but user validation failed: " + err.Error()
		if status.QrCodeStatus != nil {
			status.Status = store.AccountStatusLogging
		}
		return status, nil
	}
	status.Status = store.AccountStatusLogged
	status.Message = "Logged in as " + user.Uname
	status.RedirectURL = "/"
	return status, nil
}

func (s *APIService) RequestQRCodeLogin(ctx context.Context) (*store.QrCodeStatus, error) {
	type qrGenerateData struct {
		URL       string `json:"url"`
		QRCodeKey string `json:"qrcode_key"`
	}
	respData, _, _, err := requestJSON[qrGenerateData](s, ctx, http.MethodGet, qrCodeGenerateAPI, nil, false)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(respData.URL) == "" || strings.TrimSpace(respData.QRCodeKey) == "" {
		return nil, errors.New("invalid qrcode response")
	}

	pngBytes, err := qrcode.Encode(respData.URL, qrcode.Medium, 280)
	if err != nil {
		return nil, err
	}
	qrImage := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)

	now := time.Now()
	state := &qrState{
		status: store.QrCodeStatus{
			QrCode:              qrImage,
			QrCodeKey:           respData.QRCodeKey,
			QrCodeEffectiveTime: 180,
			IsScaned:            false,
			IsLogged:            false,
			Index:               1,
			Message:             "二维码生成成功，等待扫码",
		},
		expireAt: now.Add(180 * time.Second),
	}
	s.mu.Lock()
	s.qrState = state
	s.mu.Unlock()

	copied := state.status
	return &copied, nil
}

func (s *APIService) getQRCodeStatus() *store.QrCodeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.qrState == nil {
		return nil
	}
	if time.Now().After(s.qrState.expireAt) {
		expired := s.qrState.status
		expired.QrCodeEffectiveTime = 0
		expired.Message = "二维码已失效"
		s.qrState = nil
		return &expired
	}

	if !s.qrState.isCompleted && time.Since(s.qrState.lastPollAt) >= 2*time.Second {
		s.pollQRCodeLocked(context.Background())
	}

	remaining := int(time.Until(s.qrState.expireAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	current := s.qrState.status
	current.QrCodeEffectiveTime = remaining
	return &current
}

func (s *APIService) pollQRCodeLocked(ctx context.Context) {
	if s.qrState == nil {
		return
	}
	s.qrState.lastPollAt = time.Now()

	pollURL := qrCodePollAPI + "?qrcode_key=" + url.QueryEscape(s.qrState.status.QrCodeKey) + "&source=main_mini"
	type qrPollData struct {
		Code         int    `json:"code"`
		Message      string `json:"message"`
		RefreshToken string `json:"refresh_token"`
	}
	pollData, _, cookies, err := requestJSON[qrPollData](s, ctx, http.MethodGet, pollURL, nil, false)
	if err != nil {
		s.logWarn("poll qrcode failed: %v", err)
		s.qrState.status.Message = "轮询二维码失败: " + err.Error()
		return
	}

	switch pollData.Code {
	case 0:
		if len(cookies) > 0 {
			merged := mergeCookieWithResponse(s.readCookieString(), cookies)
			_ = s.store.SaveCookie(context.Background(), merged, pollData.RefreshToken)
		} else {
			_ = s.store.SaveRefreshToken(context.Background(), pollData.RefreshToken)
		}
		s.logInfo("qrcode login succeeded")
		s.qrState.status.IsLogged = true
		s.qrState.status.IsScaned = true
		s.qrState.status.Message = "登录成功"
		s.qrState.status.RefreshToken = pollData.RefreshToken
		s.qrState.isCompleted = true
	case 86090:
		s.qrState.status.IsScaned = true
		s.qrState.status.Message = "二维码已扫码，待确认"
	case 86101:
		s.qrState.status.IsScaned = false
		s.qrState.status.Message = "二维码未扫码"
	case 86038:
		s.qrState.status.Message = "二维码已失效"
		s.qrState.expireAt = time.Now()
	default:
		s.qrState.status.Message = fmt.Sprintf("二维码状态异常: code=%d, message=%s", pollData.Code, pollData.Message)
	}
}

func (s *APIService) SetCookie(ctx context.Context, content string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.store.SaveCookieContent(ctx, strings.TrimSpace(content))
}

func (s *APIService) Logout(ctx context.Context) error {
	s.mu.Lock()
	s.qrState = nil
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	return s.store.SaveCookie(ctx, "", "")
}

func (s *APIService) GetStreamURL(ctx context.Context, live *store.LiveSetting) (string, error) {
	s.mu.RLock()
	manual := s.manualStreamURL
	s.mu.RUnlock()
	if strings.TrimSpace(manual) != "" {
		return strings.TrimSpace(manual), nil
	}

	if live == nil {
		return "", errors.New("live setting is nil")
	}
	if need, err := s.CookieNeedToRefresh(ctx); err == nil && need {
		if refreshErr := s.RefreshCookie(ctx); refreshErr != nil {
			s.logWarn("cookie refresh before start live failed: %v", refreshErr)
			return "", refreshErr
		}
	}
	myRoom, err := s.GetMyLiveRoomInfo(ctx)
	if err != nil {
		s.logWarn("get my live room info failed, fallback to local settings: %v", err)
		myRoom = &store.MyLiveRoomInfo{
			RoomID:   live.RoomID,
			AreaV2ID: live.AreaID,
			Title:    live.RoomName,
		}
	}

	roomID := live.RoomID
	if roomID <= 0 {
		roomID = myRoom.RoomID
	}
	areaID := live.AreaID
	if areaID <= 0 {
		areaID = myRoom.AreaV2ID
	}
	title := strings.TrimSpace(live.RoomName)
	if title == "" {
		title = strings.TrimSpace(myRoom.Title)
	}
	if title != "" && (title != myRoom.Title || areaID != myRoom.AreaV2ID) {
		_ = s.UpdateLiveRoomInfo(ctx, roomID, title, areaID)
	}

	addr, code, err := s.startLive(ctx, roomID, areaID)
	if err != nil {
		s.logError("start live api failed: %v", err)
		s.mu.RLock()
		manualFallback := s.manualStreamURL
		s.mu.RUnlock()
		if strings.TrimSpace(manualFallback) != "" {
			s.logWarn("fallback to manual stream url due startLive failure")
			return strings.TrimSpace(manualFallback), nil
		}
		return "", err
	}
	streamURL := addr + code
	if strings.TrimSpace(streamURL) == "" {
		return "", errors.New("empty stream url from start live")
	}
	return streamURL, nil
}

func (s *APIService) GetMyLiveRoomInfo(ctx context.Context) (*store.MyLiveRoomInfo, error) {
	data, _, _, err := requestJSON[store.MyLiveRoomInfo](s, ctx, http.MethodGet, getMyLiveRoomInfoAPI, nil, true)
	if err != nil {
		s.logWarn("get my live room via primary api failed, trying compatibility fallback: %v", err)
		fallback, fallbackErr := s.getMyLiveRoomInfoFallback(ctx)
		if fallbackErr != nil {
			s.logError("get my live room fallback failed: %v", fallbackErr)
			return nil, err
		}
		return fallback, nil
	}
	return &data, nil
}

func (s *APIService) GetLiveAreas(ctx context.Context) ([]store.LiveAreaItem, error) {
	s.mu.RLock()
	if len(s.areas) > 0 && time.Now().Before(s.areasExpireAt) {
		cached := make([]store.LiveAreaItem, len(s.areas))
		copy(cached, s.areas)
		s.mu.RUnlock()
		return cached, nil
	}
	s.mu.RUnlock()

	areas, _, _, err := requestJSON[[]store.LiveAreaItem](s, ctx, http.MethodGet, getLiveAreaAPI, nil, false)
	if err != nil {
		s.mu.RLock()
		cached := make([]store.LiveAreaItem, len(s.areas))
		copy(cached, s.areas)
		s.mu.RUnlock()
		if len(cached) > 0 {
			s.logWarn("get live areas failed, using cached result: %v", err)
			return cached, nil
		}
		return nil, err
	}
	s.mu.Lock()
	s.areas = areas
	s.areasExpireAt = time.Now().Add(1 * time.Hour)
	s.mu.Unlock()
	return areas, nil
}

func (s *APIService) UpdateLiveRoomInfo(ctx context.Context, roomID int64, title string, areaID int) error {
	if roomID <= 0 {
		return errors.New("room id must be greater than zero")
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("title is required")
	}
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("room_id", strconv.FormatInt(roomID, 10))
	form.Set("title", title)
	form.Set("area_id", strconv.Itoa(areaID))
	form.Set("csrf", csrf)
	form.Set("csrf_token", csrf)
	_, _, _, err = requestJSON[json.RawMessage](s, ctx, http.MethodPost, updateLiveRoomInfoAPI, form, true)
	if err != nil {
		s.logWarn("update live room info failed room=%d area=%d title=%s err=%v", roomID, areaID, title, err)
	}
	return err
}

func (s *APIService) UpdateRoomNews(ctx context.Context, roomID int64, content string) error {
	if roomID <= 0 {
		return errors.New("room id must be greater than zero")
	}
	if strings.TrimSpace(content) == "" {
		return errors.New("content is required")
	}
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return err
	}
	cookieMap := parseCookieString(s.readCookieString())
	uid := cookieMap["DedeUserID"]
	if uid == "" {
		if user, userErr := s.getUserInfo(ctx); userErr == nil {
			uid = strconv.FormatInt(user.Mid, 10)
		}
	}
	form := url.Values{}
	form.Set("room_id", strconv.FormatInt(roomID, 10))
	form.Set("uid", uid)
	form.Set("content", content)
	form.Set("csrf", csrf)
	form.Set("csrf_token", csrf)
	_, _, _, err = requestJSON[json.RawMessage](s, ctx, http.MethodPost, updateRoomNewsAPI, form, true)
	if err != nil {
		s.logWarn("update room news failed room=%d err=%v", roomID, err)
	}
	return err
}

func (s *APIService) StopLive(ctx context.Context, roomID int64) error {
	if roomID <= 0 {
		return errors.New("room id must be greater than zero")
	}
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return err
	}
	form := url.Values{}
	cfg := s.readConfig()
	form.Set("platform", cfg.BiliPlatform)
	form.Set("room_id", strconv.FormatInt(roomID, 10))
	form.Set("csrf", csrf)
	form.Set("csrf_token", csrf)
	_, _, _, err = requestJSON[json.RawMessage](s, ctx, http.MethodPost, stopLiveAPI, form, true)
	if err != nil {
		s.logWarn("stop live failed room=%d err=%v", roomID, err)
	}
	return err
}

func (s *APIService) SendDanmaku(ctx context.Context, roomID int64, message string) (map[string]any, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, errors.New("message is required")
	}
	if roomID <= 0 {
		if live, err := s.store.GetLiveSetting(ctx); err == nil && live.RoomID > 0 {
			roomID = live.RoomID
		}
	}
	if roomID <= 0 {
		if roomInfo, err := s.GetMyLiveRoomInfo(ctx); err == nil && roomInfo.RoomID > 0 {
			roomID = roomInfo.RoomID
		}
	}
	if roomID <= 0 {
		return nil, errors.New("room id must be greater than zero")
	}
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return nil, err
	}

	rnd := time.Now().Unix()
	form := url.Values{}
	form.Set("bubble", "0")
	form.Set("msg", message)
	form.Set("color", "16777215")
	form.Set("mode", "1")
	form.Set("fontsize", "25")
	form.Set("rnd", strconv.FormatInt(rnd, 10))
	form.Set("roomid", strconv.FormatInt(roomID, 10))
	form.Set("csrf", csrf)
	form.Set("csrf_token", csrf)
	form.Set("room_type", "0")
	form.Set("jumpfrom", "0")
	form.Set("reply_mid", "0")
	form.Set("reply_attr", "0")
	form.Set("replay_dmid", "0")
	form.Set("statistics", `{"appId":100,"platform":5}`)
	form.Set("bp", "0")

	endpoints := []string{sendDanmakuAPI, sendDanmakuAPIAlt}
	var lastErr error
	for idx, endpoint := range endpoints {
		_, _, _, callErr := requestJSON[json.RawMessage](s, ctx, http.MethodPost, endpoint, form, true)
		if callErr != nil {
			lastErr = callErr
			s.logWarn("send danmaku failed endpoint=%s room=%d attempt=%d err=%v", endpoint, roomID, idx+1, callErr)
			continue
		}
		s.logInfo("send danmaku succeeded endpoint=%s room=%d", endpoint, roomID)
		return map[string]any{
			"roomId":   roomID,
			"message":  message,
			"endpoint": endpoint,
			"rnd":      rnd,
			"time":     time.Now().UTC().Format(time.RFC3339),
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("send danmaku failed with unknown error")
	}
	return nil, lastErr
}

func (s *APIService) CookieNeedToRefresh(ctx context.Context) (bool, error) {
	type cookieInfoData struct {
		Refresh bool `json:"refresh"`
	}
	data, _, _, err := requestJSON[cookieInfoData](s, ctx, http.MethodGet, cookieInfoAPI, nil, true)
	if err != nil {
		s.logWarn("cookie need refresh check failed: %v", err)
		return false, err
	}
	return data.Refresh, nil
}

func (s *APIService) RefreshCookie(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	setting, err := s.store.GetCookieSetting(ctx)
	if err != nil {
		return err
	}
	if strings.TrimSpace(setting.Content) == "" {
		return errors.New("cannot refresh cookie: cookie is empty")
	}
	if strings.TrimSpace(setting.RefreshToken) == "" {
		return errors.New("cannot refresh cookie: refresh token is empty")
	}
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return err
	}

	refreshCSRF, err := s.getRefreshCSRF(ctx)
	if err != nil {
		return err
	}

	oldRefreshToken := setting.RefreshToken
	form := url.Values{}
	form.Set("csrf", csrf)
	form.Set("refresh_csrf", refreshCSRF)
	form.Set("source", "main_web")
	form.Set("refresh_token", oldRefreshToken)

	type refreshCookieData struct {
		RefreshToken string `json:"refresh_token"`
	}
	data, _, cookies, err := requestJSON[refreshCookieData](s, ctx, http.MethodPost, refreshCookieAPI, form, true)
	if err != nil {
		s.logError("refresh cookie request failed: %v", err)
		return err
	}
	if strings.TrimSpace(data.RefreshToken) == "" {
		return errors.New("refresh cookie failed: empty refresh_token in response")
	}
	merged := mergeCookieWithResponse(setting.Content, cookies)
	if err := s.store.SaveCookie(ctx, merged, data.RefreshToken); err != nil {
		return err
	}

	confirmForm := url.Values{}
	confirmForm.Set("csrf", csrf)
	confirmForm.Set("refresh_token", oldRefreshToken)
	if _, _, _, err := requestJSON[json.RawMessage](s, ctx, http.MethodPost, confirmRefreshAPI, confirmForm, true); err != nil {
		s.logError("confirm refresh cookie failed: %v", err)
		return err
	}
	s.logInfo("cookie refresh succeeded")
	return nil
}

type userInfo struct {
	IsLogin bool   `json:"isLogin"`
	Mid     int64  `json:"mid"`
	Uname   string `json:"uname"`
}

type roomInfoOldData struct {
	RoomStatus int    `json:"roomStatus"`
	LiveStatus int    `json:"live_status"`
	Title      string `json:"title"`
	RoomID     int64  `json:"roomid"`
}

type roomInfoData struct {
	UID            int64  `json:"uid"`
	RoomID         int64  `json:"room_id"`
	Title          string `json:"title"`
	LiveStatus     int    `json:"live_status"`
	AreaID         int    `json:"area_id"`
	ParentAreaName string `json:"parent_area_name"`
	AreaName       string `json:"area_name"`
}

func (s *APIService) getUserInfo(ctx context.Context) (*userInfo, error) {
	data, _, _, err := requestJSON[userInfo](s, ctx, http.MethodGet, navAPI, nil, true)
	if err != nil {
		return nil, err
	}
	if !data.IsLogin {
		return nil, errors.New("user is not logged in")
	}
	return &data, nil
}

func (s *APIService) getMyLiveRoomInfoFallback(ctx context.Context) (*store.MyLiveRoomInfo, error) {
	user, err := s.getUserInfo(ctx)
	if err != nil {
		return nil, err
	}
	infoURL := getRoomInfoOldAPI + "?mid=" + strconv.FormatInt(user.Mid, 10)
	roomOld, _, _, err := requestJSON[roomInfoOldData](s, ctx, http.MethodGet, infoURL, nil, false)
	if err != nil {
		return nil, err
	}
	if roomOld.RoomStatus == 0 || roomOld.RoomID <= 0 {
		return nil, errors.New("fallback get room info failed: room not initialized")
	}

	info, roomErr := s.getRoomInfoByRoomID(ctx, roomOld.RoomID)
	if roomErr != nil {
		s.logWarn("fallback room detail query failed, using coarse room data: %v", roomErr)
		return &store.MyLiveRoomInfo{
			RoomID:     roomOld.RoomID,
			UID:        user.Mid,
			Uname:      user.Uname,
			Title:      roomOld.Title,
			LiveStatus: roomOld.LiveStatus,
		}, nil
	}
	if info.UID <= 0 {
		info.UID = user.Mid
	}
	if strings.TrimSpace(info.Uname) == "" {
		info.Uname = user.Uname
	}
	if strings.TrimSpace(info.Title) == "" {
		info.Title = roomOld.Title
	}
	if info.LiveStatus <= 0 {
		info.LiveStatus = roomOld.LiveStatus
	}
	return info, nil
}

func (s *APIService) getRoomInfoByRoomID(ctx context.Context, roomID int64) (*store.MyLiveRoomInfo, error) {
	if roomID <= 0 {
		return nil, errors.New("room id is required")
	}
	infoURL := getRoomInfoAPI + "?room_id=" + strconv.FormatInt(roomID, 10)
	data, _, _, err := requestJSON[roomInfoData](s, ctx, http.MethodGet, infoURL, nil, false)
	if err != nil {
		return nil, err
	}
	return &store.MyLiveRoomInfo{
		RoomID:     data.RoomID,
		UID:        data.UID,
		Title:      data.Title,
		AreaV2ID:   data.AreaID,
		LiveStatus: data.LiveStatus,
		ParentName: data.ParentAreaName,
		AreaV2Name: data.AreaName,
	}, nil
}

func (s *APIService) startLive(ctx context.Context, roomID int64, areaID int) (string, string, error) {
	csrf, err := s.getCsrf(ctx)
	if err != nil {
		return "", "", err
	}
	cfg := s.readConfig()
	version := strings.TrimSpace(cfg.BiliVersion)
	build := strings.TrimSpace(cfg.BiliBuild)
	if version == "" || build == "" {
		v, b, verErr := s.fetchLiveVersion(ctx)
		if verErr == nil {
			version = v
			build = b
		}
	}

	form := url.Values{}
	form.Set("room_id", strconv.FormatInt(roomID, 10))
	form.Set("platform", cfg.BiliPlatform)
	form.Set("area_v2", strconv.Itoa(areaID))
	form.Set("backup_stream", "0")
	form.Set("csrf", csrf)
	form.Set("csrf_token", csrf)
	form.Set("ts", strconv.FormatInt(time.Now().Unix(), 10))
	form.Set("type", "2")
	if version != "" {
		form.Set("version", version)
	}
	if build != "" {
		form.Set("build", build)
	}
	if cfg.BiliAppKey != "" && cfg.BiliAppSecret != "" {
		signParams := map[string]string{}
		for key, values := range form {
			if len(values) > 0 {
				signParams[key] = values[0]
			}
		}
		sign := buildAppSign(cfg.BiliAppKey, cfg.BiliAppSecret, signParams)
		form.Set("appkey", cfg.BiliAppKey)
		form.Set("sign", sign)
	}

	type startLiveData struct {
		NeedFaceAuth bool `json:"need_face_auth"`
		RTMP         struct {
			Addr string `json:"addr"`
			Code string `json:"code"`
		} `json:"rtmp"`
	}
	data, _, _, err := requestJSON[startLiveData](s, ctx, http.MethodPost, startLiveAPI, form, true)
	if err != nil {
		return "", "", err
	}
	if data.NeedFaceAuth {
		return "", "", errors.New("start live failed: need face auth")
	}
	return data.RTMP.Addr, data.RTMP.Code, nil
}

func (s *APIService) fetchLiveVersion(ctx context.Context) (string, string, error) {
	cfg := s.readConfig()
	if cfg.BiliAppKey == "" || cfg.BiliAppSecret == "" {
		return "", "", errors.New("bili app key/secret not configured")
	}
	params := map[string]string{
		"system_version": "2",
		"ts":             strconv.FormatInt(time.Now().Unix(), 10),
	}
	sign := buildAppSign(cfg.BiliAppKey, cfg.BiliAppSecret, params)
	params["appkey"] = cfg.BiliAppKey
	params["sign"] = sign
	query := url.Values{}
	for k, v := range params {
		query.Set(k, v)
	}
	urlWithQuery := liveVersionAPI + "?" + query.Encode()
	type liveVersionData struct {
		CurrVersion string `json:"curr_version"`
		Build       int    `json:"build"`
	}
	data, _, _, err := requestJSON[liveVersionData](s, ctx, http.MethodGet, urlWithQuery, nil, false)
	if err != nil {
		return "", "", err
	}
	return data.CurrVersion, strconv.Itoa(data.Build), nil
}

func (s *APIService) getCsrf(ctx context.Context) (string, error) {
	cookieSetting, err := s.store.GetCookieSetting(ctx)
	if err != nil {
		return "", err
	}
	cookies := parseCookieString(cookieSetting.Content)
	csrf := strings.TrimSpace(cookies["bili_jct"])
	if csrf == "" {
		return "", errors.New("missing bili_jct in cookie")
	}
	return csrf, nil
}

func (s *APIService) getRefreshCSRF(ctx context.Context) (string, error) {
	correspondPath, err := buildCorrespondPath()
	if err != nil {
		return "", err
	}
	targetURL := fmt.Sprintf(getRefreshCsrfAPI, correspondPath)
	body, err := requestRaw(s, ctx, http.MethodGet, targetURL, nil, true)
	if err != nil {
		return "", err
	}
	matcher := regexp.MustCompile(`<div id="1-name">(?s:(.*?))</div>`)
	found := matcher.FindStringSubmatch(body)
	if len(found) < 2 {
		return "", errors.New("get refresh csrf failed")
	}
	value := strings.TrimSpace(found[1])
	if value == "" {
		return "", errors.New("empty refresh csrf")
	}
	return value, nil
}

func (s *APIService) readCookieString() string {
	cookieSetting, err := s.store.GetCookieSetting(context.Background())
	if err != nil {
		return ""
	}
	return cookieSetting.Content
}

func encodeForm(form url.Values) string {
	if len(form) == 0 {
		return ""
	}
	return form.Encode()
}

func headerToJSON(header http.Header) string {
	if len(header) == 0 {
		return ""
	}
	normalized := map[string][]string{}
	for key, values := range header {
		copied := make([]string, len(values))
		copy(copied, values)
		normalized[key] = copied
	}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return `{"marshalError":"` + strings.ReplaceAll(err.Error(), `"`, `'`) + `"}`
	}
	return string(payload)
}

func parseHTTPStatusFromDetail(detail string) int {
	lower := strings.ToLower(strings.TrimSpace(detail))
	if lower == "" {
		return 0
	}
	if idx := strings.Index(lower, "http status "); idx >= 0 {
		raw := lower[idx+len("http status "):]
		if len(raw) >= 3 {
			if status, err := strconv.Atoi(raw[:3]); err == nil {
				return status
			}
		}
	}
	matched := regexp.MustCompile(`\bstatus[=\s:]+(\d{3})\b`).FindStringSubmatch(lower)
	if len(matched) == 2 {
		status, _ := strconv.Atoi(matched[1])
		return status
	}
	return 0
}

func toAPIErrorReport(err error, endpoint string, method string, form url.Values) apiErrorReport {
	report := apiErrorReport{
		Endpoint:    strings.TrimSpace(endpoint),
		Method:      strings.ToUpper(strings.TrimSpace(method)),
		Stage:       "unknown",
		RequestForm: encodeForm(form),
	}
	if err == nil {
		return report
	}

	var apiErr *bilibiliAPIError
	if errors.As(err, &apiErr) && apiErr != nil {
		report = apiErr.report
		if strings.TrimSpace(report.Endpoint) == "" {
			report.Endpoint = strings.TrimSpace(endpoint)
		}
		if strings.TrimSpace(report.Method) == "" {
			report.Method = strings.ToUpper(strings.TrimSpace(method))
		}
		if strings.TrimSpace(report.Stage) == "" {
			report.Stage = "unknown"
		}
		if strings.TrimSpace(report.RequestForm) == "" {
			report.RequestForm = encodeForm(form)
		}
		if strings.TrimSpace(report.Detail) == "" {
			report.Detail = strings.TrimSpace(err.Error())
		}
		return report
	}

	detail := strings.TrimSpace(err.Error())
	report.Detail = detail
	report.HTTPStatus = parseHTTPStatusFromDetail(detail)
	if idx := strings.Index(detail, " body="); idx >= 0 {
		report.ResponseBody = strings.TrimSpace(detail[idx+len(" body="):])
	}
	if report.Detail == "" {
		report.Detail = "unknown bilibili api error"
	}
	return report
}

func requestRaw(s *APIService, ctx context.Context, method string, targetURL string, form url.Values, withCookie bool) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		body, err := requestRawOnce(s, ctx, method, targetURL, form, withCookie)
		if err == nil {
			return body, nil
		}
		lastErr = err
		report := toAPIErrorReport(err, targetURL, method, form)
		report.Attempt = attempt
		report.Retryable = shouldRetryAPIError(report)
		s.logError("raw api call failed attempt=%d method=%s url=%s stage=%s status=%d retryable=%v err=%s",
			attempt, report.Method, report.Endpoint, report.Stage, report.HTTPStatus, report.Retryable, report.Detail)
		if report.ResponseHeaders != "" {
			s.logError("raw api response headers: %s", report.ResponseHeaders)
		}
		if report.ResponseBody != "" {
			s.logError("raw api full response body: %s", report.ResponseBody)
		}
		s.recordAPIError(report)
		if attempt == 2 || !report.Retryable {
			break
		}
		time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
	}
	return "", lastErr
}

func requestRawOnce(s *APIService, ctx context.Context, method string, targetURL string, form url.Values, withCookie bool) (string, error) {
	var body io.Reader
	if method == http.MethodPost && form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return "", &bilibiliAPIError{report: apiErrorReport{
			Endpoint:    targetURL,
			Method:      method,
			Stage:       "build_request",
			RequestForm: encodeForm(form),
			Detail:      err.Error(),
		}}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://www.bilibili.com")
		req.Header.Set("Referer", "https://www.bilibili.com/")
	}
	if withCookie {
		cookieContent := s.readCookieString()
		if strings.TrimSpace(cookieContent) == "" {
			return "", &bilibiliAPIError{report: apiErrorReport{
				Endpoint:    targetURL,
				Method:      method,
				Stage:       "precheck",
				RequestForm: encodeForm(form),
				Detail:      "cookie is empty",
			}}
		}
		req.Header.Set("Cookie", cookieContent)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return "", &bilibiliAPIError{report: apiErrorReport{
			Endpoint:    targetURL,
			Method:      method,
			Stage:       "network",
			RequestForm: encodeForm(form),
			Detail:      err.Error(),
		}}
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", &bilibiliAPIError{report: apiErrorReport{
			Endpoint:        targetURL,
			Method:          method,
			Stage:           "read_response",
			HTTPStatus:      resp.StatusCode,
			RequestForm:     encodeForm(form),
			ResponseHeaders: headerToJSON(resp.Header),
			Detail:          err.Error(),
		}}
	}
	bodyText := string(bodyBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &bilibiliAPIError{report: apiErrorReport{
			Endpoint:        targetURL,
			Method:          method,
			Stage:           "http_status",
			HTTPStatus:      resp.StatusCode,
			RequestForm:     encodeForm(form),
			ResponseHeaders: headerToJSON(resp.Header),
			ResponseBody:    bodyText,
			Detail:          fmt.Sprintf("http status %d", resp.StatusCode),
		}}
	}
	return bodyText, nil
}

type bilibiliEnvelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Msg     string          `json:"msg"`
	Data    json.RawMessage `json:"data"`
	Result  json.RawMessage `json:"result"`
}

func requestJSON[T any](s *APIService, ctx context.Context, method string, targetURL string, form url.Values, withCookie bool) (T, http.Header, []*http.Cookie, error) {
	var zero T
	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	var lastHeader http.Header
	var lastCookies []*http.Cookie
	for attempt := 1; attempt <= 2; attempt++ {
		data, header, cookies, err := requestJSONOnce[T](s, ctx, method, targetURL, form, withCookie)
		if err == nil {
			return data, header, cookies, nil
		}
		lastErr = err
		lastHeader = header
		lastCookies = cookies

		report := toAPIErrorReport(err, targetURL, method, form)
		report.Attempt = attempt
		report.Retryable = shouldRetryAPIError(report)
		s.logError("api call failed attempt=%d method=%s url=%s stage=%s status=%d retryable=%v err=%s",
			attempt, report.Method, report.Endpoint, report.Stage, report.HTTPStatus, report.Retryable, report.Detail)
		if report.ResponseHeaders != "" {
			s.logError("api response headers: %s", report.ResponseHeaders)
		}
		if report.ResponseBody != "" {
			s.logError("api full response body: %s", report.ResponseBody)
		}
		s.recordAPIError(report)

		if attempt == 2 || !report.Retryable {
			break
		}
		time.Sleep(time.Duration(attempt) * 250 * time.Millisecond)
	}
	return zero, lastHeader, lastCookies, lastErr
}

func requestJSONOnce[T any](s *APIService, ctx context.Context, method string, targetURL string, form url.Values, withCookie bool) (T, http.Header, []*http.Cookie, error) {
	var zero T
	var body io.Reader
	if method == http.MethodPost && form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, targetURL, body)
	if err != nil {
		return zero, nil, nil, &bilibiliAPIError{report: apiErrorReport{
			Endpoint:    targetURL,
			Method:      method,
			Stage:       "build_request",
			RequestForm: encodeForm(form),
			Detail:      err.Error(),
		}}
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://live.bilibili.com")
		req.Header.Set("Referer", "https://live.bilibili.com/")
	}
	if withCookie {
		cookieContent := s.readCookieString()
		if strings.TrimSpace(cookieContent) == "" {
			return zero, nil, nil, &bilibiliAPIError{report: apiErrorReport{
				Endpoint:    targetURL,
				Method:      method,
				Stage:       "precheck",
				RequestForm: encodeForm(form),
				Detail:      "cookie is empty",
			}}
		}
		req.Header.Set("Cookie", cookieContent)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return zero, nil, nil, &bilibiliAPIError{report: apiErrorReport{
			Endpoint:    targetURL,
			Method:      method,
			Stage:       "network",
			RequestForm: encodeForm(form),
			Detail:      err.Error(),
		}}
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return zero, resp.Header.Clone(), resp.Cookies(), &bilibiliAPIError{report: apiErrorReport{
			Endpoint:        targetURL,
			Method:          method,
			Stage:           "read_response",
			HTTPStatus:      resp.StatusCode,
			RequestForm:     encodeForm(form),
			ResponseHeaders: headerToJSON(resp.Header),
			Detail:          err.Error(),
		}}
	}
	bodyText := string(bodyBytes)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return zero, resp.Header.Clone(), resp.Cookies(), &bilibiliAPIError{report: apiErrorReport{
			Endpoint:        targetURL,
			Method:          method,
			Stage:           "http_status",
			HTTPStatus:      resp.StatusCode,
			RequestForm:     encodeForm(form),
			ResponseHeaders: headerToJSON(resp.Header),
			ResponseBody:    bodyText,
			Detail:          fmt.Sprintf("http status %d", resp.StatusCode),
		}}
	}
	parsed, err := decodeEnvelopeData[T](bodyBytes)
	if err != nil {
		stage := "decode_response"
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "bilibili api error code=") {
			stage = "api_code"
		}
		return zero, resp.Header.Clone(), resp.Cookies(), &bilibiliAPIError{report: apiErrorReport{
			Endpoint:        targetURL,
			Method:          method,
			Stage:           stage,
			HTTPStatus:      resp.StatusCode,
			RequestForm:     encodeForm(form),
			ResponseHeaders: headerToJSON(resp.Header),
			ResponseBody:    bodyText,
			Detail:          err.Error(),
		}}
	}
	return parsed, resp.Header.Clone(), resp.Cookies(), nil
}

func decodeEnvelopeData[T any](bodyBytes []byte) (T, error) {
	var zero T
	var root map[string]json.RawMessage
	if err := json.Unmarshal(bodyBytes, &root); err != nil {
		// Compatibility fallback: some endpoints may return direct payload without code/message envelope.
		if direct, directErr := decodePossibleDirectData[T](bodyBytes); directErr == nil {
			return direct, nil
		}
		return zero, err
	}

	_, hasCode := root["code"]
	dataPayload := bytes.TrimSpace(root["data"])
	resultPayload := bytes.TrimSpace(root["result"])
	if !hasCode && len(dataPayload) == 0 && len(resultPayload) == 0 {
		// Direct payload response without envelope.
		return decodePossibleDirectData[T](bodyBytes)
	}

	var env bilibiliEnvelope
	if err := json.Unmarshal(bodyBytes, &env); err != nil {
		return zero, err
	}
	if hasCode && env.Code != 0 {
		message := strings.TrimSpace(env.Message)
		if message == "" {
			message = strings.TrimSpace(env.Msg)
		}
		if message == "" {
			message = "unknown bilibili api error"
		}
		return zero, fmt.Errorf("bilibili api error code=%d message=%s", env.Code, message)
	}

	payload := bytes.TrimSpace(env.Data)
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		payload = bytes.TrimSpace(env.Result)
	}
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		var emptyStruct struct{}
		if _, ok := any(zero).(json.RawMessage); ok {
			return any(json.RawMessage("{}")).(T), nil
		}
		if _, ok := any(zero).(struct{}); ok {
			return any(emptyStruct).(T), nil
		}
		return zero, nil
	}

	if data, err := decodePossibleDirectData[T](payload); err == nil {
		return data, nil
	}

	var container map[string]json.RawMessage
	if err := json.Unmarshal(payload, &container); err == nil {
		for _, key := range []string{"list", "items", "result", "room_info", "roomInfo", "info"} {
			if nested, exists := container[key]; exists {
				if data, nestedErr := decodePossibleDirectData[T](nested); nestedErr == nil {
					return data, nil
				}
			}
		}
	}
	return zero, errors.New("unsupported bilibili response shape")
}

func decodePossibleDirectData[T any](payload []byte) (T, error) {
	var out T
	if err := json.Unmarshal(payload, &out); err != nil {
		return out, err
	}
	return out, nil
}

func shouldRetryAPIError(report apiErrorReport) bool {
	if report.HTTPStatus == http.StatusTooManyRequests || report.HTTPStatus == http.StatusRequestTimeout {
		return true
	}
	if report.HTTPStatus >= 500 {
		return true
	}
	if report.Stage == "network" || report.Stage == "read_response" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(report.Detail))
	if strings.Contains(lower, "timeout") || strings.Contains(lower, "temporarily") || strings.Contains(lower, "connection reset") {
		return true
	}
	if strings.Contains(lower, "eof") {
		return true
	}
	if strings.Contains(lower, "tls handshake timeout") {
		return true
	}
	return false
}

func buildAppSign(appKey string, appSecret string, params map[string]string) string {
	paramsCopy := map[string]string{}
	for k, v := range params {
		paramsCopy[k] = v
	}
	paramsCopy["appkey"] = appKey
	keys := make([]string, 0, len(paramsCopy))
	for key := range paramsCopy {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	query := url.Values{}
	for _, key := range keys {
		query.Set(key, paramsCopy[key])
	}
	signing := query.Encode() + appSecret
	h := md5.Sum([]byte(signing))
	return hex.EncodeToString(h[:])
}

func parseCookieString(raw string) map[string]string {
	result := map[string]string{}
	for _, token := range strings.Split(raw, ";") {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		eq := strings.Index(token, "=")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(token[:eq])
		value := strings.TrimSpace(token[eq+1:])
		if name == "" {
			continue
		}
		result[name] = value
	}
	return result
}

func mergeCookieWithResponse(existing string, newCookies []*http.Cookie) string {
	cookieMap := parseCookieString(existing)
	for _, cookie := range newCookies {
		if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
			continue
		}
		cookieMap[cookie.Name] = cookie.Value
	}
	keys := make([]string, 0, len(cookieMap))
	for key := range cookieMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+cookieMap[key])
	}
	return strings.Join(parts, "; ")
}

func buildCorrespondPath() (string, error) {
	block, _ := pem.Decode([]byte(refreshPublicKeyPEM))
	if block == nil {
		return "", errors.New("parse refresh public key failed")
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return "", err
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		return "", errors.New("refresh public key type invalid")
	}
	ts := time.Now().UTC().Add(-1 * time.Minute).UnixMilli()
	content := "refresh_" + strconv.FormatInt(ts, 10)
	cipher, err := rsa.EncryptOAEP(sha256.New(), cryptorand.Reader, pub, []byte(content), nil)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(cipher), nil
}
