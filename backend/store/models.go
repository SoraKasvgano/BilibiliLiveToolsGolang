package store

import (
	"strings"
	"time"
)

type ConfigModel int

const (
	ConfigModelNone    ConfigModel = 0
	ConfigModelNormal  ConfigModel = 1
	ConfigModelAdvance ConfigModel = 2
)

type OutputQuality int

const (
	OutputQualityHigh     OutputQuality = 1
	OutputQualityMedium   OutputQuality = 2
	OutputQualityLow      OutputQuality = 3
	OutputQualityOriginal OutputQuality = 9
)

type PushStatus int

const (
	PushStatusStarting PushStatus = 1
	PushStatusStopped  PushStatus = 2
	PushStatusRunning  PushStatus = 3
	PushStatusWaiting  PushStatus = 4
)

type FileType int

const (
	FileTypeUnknown FileType = 0
	FileTypeVideo   FileType = 1
	FileTypeMusic   FileType = 2
)

type InputType string

const (
	InputTypeVideo      InputType = "video"
	InputTypeDesktop    InputType = "desktop"
	InputTypeUSBCamera  InputType = "usb_camera"
	InputTypeCameraPlus InputType = "usb_camera_plus"
	InputTypeRTSP       InputType = "rtsp"
	InputTypeMJPEG      InputType = "mjpeg"
	InputTypeONVIF      InputType = "onvif"
)

type InputAudioSource string

const (
	InputAudioSourceFile   InputAudioSource = "file"
	InputAudioSourceDevice InputAudioSource = "device"
)

func NormalizeInputType(newType string, legacyType int) InputType {
	if strings.TrimSpace(newType) != "" {
		switch InputType(strings.ToLower(strings.TrimSpace(newType))) {
		case InputTypeVideo, InputTypeDesktop, InputTypeUSBCamera, InputTypeCameraPlus, InputTypeRTSP, InputTypeMJPEG, InputTypeONVIF:
			return InputType(strings.ToLower(strings.TrimSpace(newType)))
		}
	}
	switch legacyType {
	case 1:
		return InputTypeVideo
	case 2:
		return InputTypeDesktop
	case 3:
		return InputTypeUSBCamera
	case 4:
		return InputTypeCameraPlus
	default:
		return InputTypeVideo
	}
}

type MultiInputSource struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	Primary    bool   `json:"primary"`
	SourceType string `json:"sourceType"`
	MaterialID int64  `json:"materialId"`
}

// Result-compatible page payload.
type QueryPageModel[T any] struct {
	Page      int   `json:"page"`
	PageCount int   `json:"pageCount"`
	DataCount int64 `json:"dataCount"`
	PageSize  int   `json:"pageSize"`
	Data      []T   `json:"data"`
}

type PushSetting struct {
	ID                    int64              `json:"id"`
	Model                 ConfigModel        `json:"model"`
	FFmpegCommand         string             `json:"ffmpegCommand"`
	IsAutoRetry           bool               `json:"isAutoRetry"`
	RetryInterval         int                `json:"retryInterval"`
	IsUpdate              bool               `json:"isUpdate"`
	InputType             InputType          `json:"inputType"`
	OutputResolution      string             `json:"outputResolution"`
	OutputQuality         OutputQuality      `json:"outputQuality"`
	OutputBitrateKbps     int                `json:"outputBitrateKbps"`
	CustomOutputParams    string             `json:"custumOutputParams"`
	CustomVideoCodec      string             `json:"custumVideoCodec"`
	VideoMaterialID       *int64             `json:"videoId"`
	AudioMaterialID       *int64             `json:"audioId"`
	IsMute                bool               `json:"isMute"`
	InputScreen           string             `json:"inputScreen"`
	InputAudioSource      InputAudioSource   `json:"inputAudioSource"`
	InputAudioDeviceName  string             `json:"inputDeviceAudioDeviceName"`
	InputDeviceName       string             `json:"inputDeviceName"`
	InputDeviceResolution string             `json:"inputDeviceResolution"`
	InputDeviceFramerate  int                `json:"inputDeviceFramerate"`
	InputDevicePlugins    string             `json:"inputDevicePlugins"`
	RTSPURL               string             `json:"rtspUrl"`
	MJPEGURL              string             `json:"mjpegUrl"`
	ONVIFEndpoint         string             `json:"onvifEndpoint"`
	ONVIFUsername         string             `json:"onvifUsername"`
	ONVIFPassword         string             `json:"onvifPassword"`
	ONVIFProfileToken     string             `json:"onvifProfileToken"`
	MultiInputEnabled     bool               `json:"multiInputEnabled"`
	MultiInputLayout      string             `json:"multiInputLayout"`
	MultiInputURLs        []string           `json:"multiInputUrls"`
	MultiInputMeta        []MultiInputSource `json:"multiInputMeta"`
	CreatedAt             time.Time          `json:"createdAt"`
	UpdatedAt             time.Time          `json:"updatedAt"`
}

type PushSettingUpdateRequest struct {
	Model                  ConfigModel        `json:"model"`
	FFmpegCommand          string             `json:"ffmpegCommand"`
	IsAutoRetry            bool               `json:"isAutoRetry"`
	RetryInterval          int                `json:"retryInterval"`
	InputType              string             `json:"inputType"`
	LegacyInputType        int                `json:"legacyInputType"`
	OutputResolution       string             `json:"outputResolution"`
	OutputQuality          OutputQuality      `json:"outputQuality"`
	OutputBitrateKbps      int                `json:"outputBitrateKbps"`
	CustomOutputParams     string             `json:"custumOutputParams"`
	CustomVideoCodec       string             `json:"custumVideoCodec"`
	VideoID                int64              `json:"videoId"`
	AudioID                int64              `json:"audioId"`
	IsMute                 bool               `json:"isMute"`
	InputScreen            string             `json:"inputScreen"`
	InputDeviceName        string             `json:"inputDeviceName"`
	InputDeviceResolution  string             `json:"inputDeviceResolution"`
	InputDeviceFramerate   int                `json:"inputDeviceFramerate"`
	InputDeviceAudioFrom   bool               `json:"inputDeviceAudioFrom"`
	InputDeviceAudioDevice string             `json:"inputDeviceAudioDeviceName"`
	InputDeviceAudioID     int64              `json:"inputDeviceAudioId"`
	InputDevicePlugins     string             `json:"inputDevicePlugins"`
	DesktopAudioFrom       bool               `json:"desktopAudioFrom"`
	DesktopAudioID         int64              `json:"desktopAudioId"`
	DesktopAudioDevice     string             `json:"desktopAudioDeviceName"`
	RTSPURL                string             `json:"rtspUrl"`
	MJPEGURL               string             `json:"mjpegUrl"`
	ONVIFEndpoint          string             `json:"onvifEndpoint"`
	ONVIFUsername          string             `json:"onvifUsername"`
	ONVIFPassword          string             `json:"onvifPassword"`
	ONVIFProfileToken      string             `json:"onvifProfileToken"`
	MultiInputEnabled      bool               `json:"multiInputEnabled"`
	MultiInputLayout       string             `json:"multiInputLayout"`
	MultiInputURLs         []string           `json:"multiInputUrls"`
	MultiInputMeta         []MultiInputSource `json:"multiInputMeta"`
}

type PushStatusResponse struct {
	Status PushStatus `json:"status"`
}

type LiveSetting struct {
	ID        int64     `json:"id"`
	AreaID    int       `json:"areaId"`
	RoomName  string    `json:"roomName"`
	RoomID    int64     `json:"roomId"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type RoomInfoUpdateRequest struct {
	AreaID   int    `json:"areaId"`
	RoomName string `json:"roomName"`
	RoomID   int64  `json:"roomId"`
}

type RoomNewUpdateRequest struct {
	RoomID  int64  `json:"roomId"`
	Content string `json:"content"`
}

type MonitorSetting struct {
	ID                  int64     `json:"id"`
	IsEnabled           bool      `json:"isEnabled"`
	RoomID              int64     `json:"roomId"`
	RoomURL             string    `json:"roomUrl"`
	IsEnableEmailNotice bool      `json:"isEnableEmailNotice"`
	SMTPServer          string    `json:"smtpServer"`
	SMTPSsl             bool      `json:"smtpSsl"`
	SMTPPort            int       `json:"smtpPort"`
	MailAddress         string    `json:"mailAddress"`
	MailName            string    `json:"mailName"`
	Password            string    `json:"password"`
	Receivers           string    `json:"receivers"`
	CreatedAt           time.Time `json:"createdAt"`
	UpdatedAt           time.Time `json:"updatedAt"`
}

type MonitorRoomInfoUpdateRequest struct {
	IsEnabled bool   `json:"isEnabled"`
	RoomID    int64  `json:"roomId"`
	RoomURL   string `json:"roomUrl"`
}

type MonitorEmailUpdateRequest struct {
	IsEnableEmailNotice bool   `json:"isEnableEmailNotice"`
	SMTPServer          string `json:"smtpServer"`
	SMTPSsl             bool   `json:"smtpSsl"`
	SMTPPort            int    `json:"smtpPort"`
	MailAddress         string `json:"mailAddress"`
	MailName            string `json:"mailName"`
	Password            string `json:"password"`
	Receivers           string `json:"receivers"`
}

type CookieSetting struct {
	ID           int64     `json:"id"`
	Content      string    `json:"content"`
	RefreshToken string    `json:"refreshToken"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Material struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	SizeKB      int64     `json:"sizeKb"`
	FileType    FileType  `json:"fileType"`
	Description string    `json:"description"`
	MediaInfo   string    `json:"mediaInfo"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type MaterialDTO struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	FullPath    string `json:"fullPath"`
	Size        string `json:"size"`
	FileType    string `json:"fileType"`
	Description string `json:"description"`
	Duration    string `json:"duration"`
	MediaInfo   string `json:"mediaInfo"`
	CreatedTime string `json:"createdTime"`
}

type MaterialListPageRequest struct {
	FileName string
	FileType FileType
	Page     int
	Limit    int
	Field    string
	Order    string
}

type FFmpegLogItem struct {
	LogType string    `json:"logType"`
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

// Placeholder entities for future integrations.
type DanmakuPTZRule struct {
	ID           int64     `json:"id"`
	Keyword      string    `json:"keyword"`
	Action       string    `json:"action"`
	PTZDirection string    `json:"ptzDirection"`
	PTZSpeed     int       `json:"ptzSpeed"`
	Enabled      bool      `json:"enabled"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type WebhookSetting struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret"`
	Enabled   bool      `json:"enabled"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type APIKeySetting struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	APIKey      string    `json:"apiKey"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type IntegrationTaskStatus string

const (
	IntegrationTaskStatusPending   IntegrationTaskStatus = "pending"
	IntegrationTaskStatusRunning   IntegrationTaskStatus = "running"
	IntegrationTaskStatusSucceeded IntegrationTaskStatus = "succeeded"
	IntegrationTaskStatusDead      IntegrationTaskStatus = "dead"
	IntegrationTaskStatusCancelled IntegrationTaskStatus = "cancelled"
)

type IntegrationTask struct {
	ID          int64                 `json:"id"`
	TaskType    string                `json:"taskType"`
	Status      IntegrationTaskStatus `json:"status"`
	Priority    int                   `json:"priority"`
	Payload     string                `json:"payload"`
	Attempt     int                   `json:"attempt"`
	MaxAttempts int                   `json:"maxAttempts"`
	NextRunAt   time.Time             `json:"nextRunAt"`
	LockedAt    *time.Time            `json:"lockedAt,omitempty"`
	LastError   string                `json:"lastError"`
	RateKey     string                `json:"rateKey"`
	CreatedAt   time.Time             `json:"createdAt"`
	UpdatedAt   time.Time             `json:"updatedAt"`
	FinishedAt  *time.Time            `json:"finishedAt,omitempty"`
}

type IntegrationTaskSummary struct {
	Pending   int64 `json:"pending"`
	Running   int64 `json:"running"`
	Succeeded int64 `json:"succeeded"`
	Dead      int64 `json:"dead"`
	Cancelled int64 `json:"cancelled"`
}

type DanmakuConsumerSetting struct {
	ID              int64      `json:"id"`
	Enabled         bool       `json:"enabled"`
	Provider        string     `json:"provider"`
	Endpoint        string     `json:"endpoint"`
	AuthToken       string     `json:"authToken"`
	ConfigJSON      string     `json:"configJson"`
	PollIntervalSec int        `json:"pollIntervalSec"`
	BatchSize       int        `json:"batchSize"`
	RoomID          int64      `json:"roomId"`
	Cursor          string     `json:"cursor"`
	LastPollAt      *time.Time `json:"lastPollAt,omitempty"`
	LastError       string     `json:"lastError"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type IntegrationFeatureSetting struct {
	ID                    int64     `json:"id"`
	SimpleMode            bool      `json:"simpleMode"`
	EnableDanmakuConsumer bool      `json:"enableDanmakuConsumer"`
	EnableWebhook         bool      `json:"enableWebhook"`
	EnableBot             bool      `json:"enableBot"`
	EnableAdvancedStats   bool      `json:"enableAdvancedStats"`
	EnableTaskQueue       bool      `json:"enableTaskQueue"`
	UpdatedAt             time.Time `json:"updatedAt"`
}

type IntegrationQueueSetting struct {
	ID               int64     `json:"id"`
	WebhookRateGapMS int       `json:"webhookRateGapMs"`
	BotRateGapMS     int       `json:"botRateGapMs"`
	MaxWorkers       int       `json:"maxWorkers"`
	LeaseIntervalMS  int       `json:"leaseIntervalMs"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

type WebhookDeliveryLog struct {
	ID             int64     `json:"id"`
	WebhookID      *int64    `json:"webhookId,omitempty"`
	WebhookName    string    `json:"webhookName"`
	EventType      string    `json:"eventType"`
	RequestBody    string    `json:"requestBody"`
	ResponseStatus int       `json:"responseStatus"`
	ResponseBody   string    `json:"responseBody"`
	Success        bool      `json:"success"`
	ErrorMessage   string    `json:"errorMessage"`
	DurationMS     int64     `json:"durationMs"`
	Attempt        int       `json:"attempt"`
	CreatedAt      time.Time `json:"createdAt"`
}

type BilibiliAPIAlertSetting struct {
	ID              int64      `json:"id"`
	Enabled         bool       `json:"enabled"`
	WindowMinutes   int        `json:"windowMinutes"`
	Threshold       int        `json:"threshold"`
	CooldownMinutes int        `json:"cooldownMinutes"`
	WebhookEvent    string     `json:"webhookEvent"`
	LastAlertAt     *time.Time `json:"lastAlertAt,omitempty"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type BilibiliAPIErrorLog struct {
	ID              int64     `json:"id"`
	Endpoint        string    `json:"endpoint"`
	Method          string    `json:"method"`
	Stage           string    `json:"stage"`
	HTTPStatus      int       `json:"httpStatus"`
	Attempt         int       `json:"attempt"`
	Retryable       bool      `json:"retryable"`
	RequestForm     string    `json:"requestForm"`
	ResponseHeaders string    `json:"responseHeaders"`
	ResponseBody    string    `json:"responseBody"`
	ErrorMessage    string    `json:"errorMessage"`
	CreatedAt       time.Time `json:"createdAt"`
}

type MaintenanceSetting struct {
	ID            int64      `json:"id"`
	Enabled       bool       `json:"enabled"`
	RetentionDays int        `json:"retentionDays"`
	AutoVacuum    bool       `json:"autoVacuum"`
	LastCleanupAt *time.Time `json:"lastCleanupAt,omitempty"`
	LastVacuumAt  *time.Time `json:"lastVacuumAt,omitempty"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type CleanupStats struct {
	LiveEvents          int64 `json:"liveEvents"`
	DanmakuRecords      int64 `json:"danmakuRecords"`
	WebhookDeliveryLogs int64 `json:"webhookDeliveryLogs"`
	BilibiliErrorLogs   int64 `json:"bilibiliErrorLogs"`
	IntegrationTasks    int64 `json:"integrationTasks"`
	StreamSessions      int64 `json:"streamSessions"`
	Total               int64 `json:"total"`
}

type DBStats struct {
	DBPath         string `json:"dbPath"`
	DBSizeBytes    int64  `json:"dbSizeBytes"`
	WALSizeBytes   int64  `json:"walSizeBytes"`
	SHMSizeBytes   int64  `json:"shmSizeBytes"`
	PageCount      int64  `json:"pageCount"`
	PageSize       int64  `json:"pageSize"`
	FreeListCount  int64  `json:"freeListCount"`
	EstimatedInUse int64  `json:"estimatedInUse"`
}

type DeviceInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type CameraSourceType string

const (
	CameraSourceTypeRTSP  CameraSourceType = "rtsp"
	CameraSourceTypeMJPEG CameraSourceType = "mjpeg"
	CameraSourceTypeONVIF CameraSourceType = "onvif"
	CameraSourceTypeUSB   CameraSourceType = "usb"
)

func NormalizeCameraSourceType(raw string) CameraSourceType {
	switch CameraSourceType(strings.ToLower(strings.TrimSpace(raw))) {
	case CameraSourceTypeRTSP, CameraSourceTypeMJPEG, CameraSourceTypeONVIF, CameraSourceTypeUSB:
		return CameraSourceType(strings.ToLower(strings.TrimSpace(raw)))
	default:
		return ""
	}
}

type CameraSource struct {
	ID                  int64            `json:"id"`
	Name                string           `json:"name"`
	SourceType          CameraSourceType `json:"sourceType"`
	RTSPURL             string           `json:"rtspUrl"`
	MJPEGURL            string           `json:"mjpegUrl"`
	ONVIFEndpoint       string           `json:"onvifEndpoint"`
	ONVIFUsername       string           `json:"onvifUsername"`
	ONVIFPassword       string           `json:"onvifPassword"`
	ONVIFProfileToken   string           `json:"onvifProfileToken"`
	USBDeviceName       string           `json:"usbDeviceName"`
	USBDeviceResolution string           `json:"usbDeviceResolution"`
	USBDeviceFramerate  int              `json:"usbDeviceFramerate"`
	Description         string           `json:"description"`
	Enabled             bool             `json:"enabled"`
	CreatedAt           time.Time        `json:"createdAt"`
	UpdatedAt           time.Time        `json:"updatedAt"`
}

func (c CameraSource) StreamURL() string {
	switch c.SourceType {
	case CameraSourceTypeRTSP, CameraSourceTypeONVIF:
		return strings.TrimSpace(c.RTSPURL)
	case CameraSourceTypeMJPEG:
		return strings.TrimSpace(c.MJPEGURL)
	default:
		return ""
	}
}

type CameraSourceListRequest struct {
	Keyword    string
	SourceType string
	Page       int
	Limit      int
}

type CameraSourceSaveRequest struct {
	ID                  int64  `json:"id"`
	Name                string `json:"name"`
	SourceType          string `json:"sourceType"`
	RTSPURL             string `json:"rtspUrl"`
	MJPEGURL            string `json:"mjpegUrl"`
	ONVIFEndpoint       string `json:"onvifEndpoint"`
	ONVIFUsername       string `json:"onvifUsername"`
	ONVIFPassword       string `json:"onvifPassword"`
	ONVIFProfileToken   string `json:"onvifProfileToken"`
	USBDeviceName       string `json:"usbDeviceName"`
	USBDeviceResolution string `json:"usbDeviceResolution"`
	USBDeviceFramerate  int    `json:"usbDeviceFramerate"`
	Description         string `json:"description"`
	Enabled             bool   `json:"enabled"`
}

type LiveEvent struct {
	ID        int64     `json:"id"`
	SessionID *int64    `json:"sessionId,omitempty"`
	EventType string    `json:"eventType"`
	Payload   string    `json:"payload"`
	CreatedAt time.Time `json:"createdAt"`
}

type DanmakuRecord struct {
	ID         int64     `json:"id"`
	RoomID     int64     `json:"roomId"`
	UID        int64     `json:"uid"`
	Uname      string    `json:"uname"`
	Content    string    `json:"content"`
	RawPayload string    `json:"rawPayload"`
	CreatedAt  time.Time `json:"createdAt"`
}

type LoginStatus struct {
	Status       int           `json:"status"`
	RedirectURL  string        `json:"redirectUrl,omitempty"`
	Message      string        `json:"message,omitempty"`
	QrCodeStatus *QrCodeStatus `json:"qrCodeStatus,omitempty"`
}

const (
	AccountStatusNotLogin = 1
	AccountStatusLogging  = 2
	AccountStatusLogged   = 3
	AccountStatusWaiting  = 4
)

type QrCodeStatus struct {
	QrCode              string `json:"qrCode"`
	QrCodeKey           string `json:"qrCodeKey,omitempty"`
	RefreshToken        string `json:"refreshToken,omitempty"`
	QrCodeEffectiveTime int    `json:"qrCodeEffectiveTime"`
	IsScaned            bool   `json:"isScaned"`
	IsLogged            bool   `json:"isLogged"`
	Index               int    `json:"index"`
	Message             string `json:"message,omitempty"`
}

type LiveAreaItem struct {
	ID         int            `json:"id"`
	ParentID   int            `json:"parent_id"`
	Name       string         `json:"name"`
	ParentName string         `json:"parent_name"`
	List       []LiveAreaItem `json:"list,omitempty"`
}

type MyLiveRoomInfo struct {
	RoomID     int64  `json:"room_id"`
	UID        int64  `json:"uid"`
	Uname      string `json:"uname"`
	Title      string `json:"title"`
	AreaV2ID   int    `json:"area_v2_id"`
	LiveStatus int    `json:"live_status"`
	HaveLive   int    `json:"have_live"`
	ParentName string `json:"parent_name"`
	AreaV2Name string `json:"area_v2_name"`
	Announce   struct {
		Content string `json:"content"`
	} `json:"announce"`
	AuditInfo struct {
		AuditTitle string `json:"audit_title"`
	} `json:"audit_info"`
}
