package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (s *Store) GetPushSetting(ctx context.Context) (*PushSetting, error) {
	const q = `SELECT
		id, model, ffmpeg_command, is_auto_retry, retry_interval, is_update,
		input_type, output_resolution, output_quality, output_bitrate_kbps, custom_output_params, custom_video_codec,
		video_material_id, audio_material_id, is_mute, input_screen, input_audio_source,
		input_audio_device_name, input_device_name, input_device_resolution, input_device_framerate,
		input_device_plugins, rtsp_url, mjpeg_url, rtmp_url, gb_pull_url, onvif_endpoint, onvif_username, onvif_password,
		onvif_profile_token, multi_input_enabled, multi_input_layout, multi_input_urls, multi_input_meta, created_at, updated_at
	FROM push_settings ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q)
	item := PushSetting{}
	var videoID sql.NullInt64
	var audioID sql.NullInt64
	var createdAt string
	var updatedAt string
	var autoRetry int
	var isUpdate int
	var isMute int
	var multiEnabled int
	var multiURLsRaw string
	var multiMetaRaw string
	if err := row.Scan(
		&item.ID,
		&item.Model,
		&item.FFmpegCommand,
		&autoRetry,
		&item.RetryInterval,
		&isUpdate,
		&item.InputType,
		&item.OutputResolution,
		&item.OutputQuality,
		&item.OutputBitrateKbps,
		&item.CustomOutputParams,
		&item.CustomVideoCodec,
		&videoID,
		&audioID,
		&isMute,
		&item.InputScreen,
		&item.InputAudioSource,
		&item.InputAudioDeviceName,
		&item.InputDeviceName,
		&item.InputDeviceResolution,
		&item.InputDeviceFramerate,
		&item.InputDevicePlugins,
		&item.RTSPURL,
		&item.MJPEGURL,
		&item.RTMPURL,
		&item.GBPullURL,
		&item.ONVIFEndpoint,
		&item.ONVIFUsername,
		&item.ONVIFPassword,
		&item.ONVIFProfileToken,
		&multiEnabled,
		&item.MultiInputLayout,
		&multiURLsRaw,
		&multiMetaRaw,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if videoID.Valid {
		item.VideoMaterialID = &videoID.Int64
	}
	if audioID.Valid {
		item.AudioMaterialID = &audioID.Int64
	}
	item.IsAutoRetry = autoRetry == 1
	item.IsUpdate = isUpdate == 1
	item.IsMute = isMute == 1
	item.MultiInputEnabled = multiEnabled == 1
	item.MultiInputURLs = parseJSONStringArray(multiURLsRaw)
	item.MultiInputMeta = parseMultiInputMeta(multiMetaRaw, item.MultiInputURLs)
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) UpdatePushSetting(ctx context.Context, req PushSettingUpdateRequest) (*PushSetting, error) {
	current, err := s.GetPushSetting(ctx)
	if err != nil {
		return nil, err
	}
	if req.RetryInterval < 30 {
		req.RetryInterval = 30
	}
	inputType := NormalizeInputType(req.InputType, req.LegacyInputType)
	if req.Model == ConfigModelAdvance {
		if strings.TrimSpace(req.FFmpegCommand) == "" {
			return nil, errors.New("ffmpeg command can not be empty")
		}
		if !strings.Contains(req.FFmpegCommand, "{URL}") {
			return nil, errors.New("ffmpeg command must include {URL}")
		}
		if isLikelyUSBTemplateCommand(req.FFmpegCommand) && (inputType == InputTypeRTSP || inputType == InputTypeMJPEG || inputType == InputTypeONVIF || inputType == InputTypeRTMP || inputType == InputTypeGB28181) {
			return nil, errors.New("advanced ffmpeg command looks like USB camera template; switch to normal mode or replace command for current input type")
		}
	}
	if req.OutputResolution == "" {
		req.OutputResolution = "1280x720"
	}
	req.OutputBitrateKbps = normalizeBitrateKbps(req.OutputBitrateKbps)
	if strings.TrimSpace(req.MultiInputLayout) == "" {
		req.MultiInputLayout = "2x2"
	}
	multiURLs := normalizeURLList(req.MultiInputURLs, 9)
	multiMeta := normalizeMultiInputMeta(req.MultiInputMeta, multiURLs, 9)
	if len(multiURLs) == 0 && len(multiMeta) > 0 {
		multiURLs = make([]string, 0, len(multiMeta))
		for _, item := range multiMeta {
			multiURLs = append(multiURLs, item.URL)
		}
	}
	multiURLsJSON := "[]"
	if body, marshalErr := json.Marshal(multiURLs); marshalErr == nil {
		multiURLsJSON = string(body)
	}
	multiMetaJSON := "[]"
	if body, marshalErr := json.Marshal(multiMeta); marshalErr == nil {
		multiMetaJSON = string(body)
	}

	var videoID sql.NullInt64
	var audioID sql.NullInt64
	if req.VideoID > 0 {
		videoID = sql.NullInt64{Int64: req.VideoID, Valid: true}
	}

	inputAudioSource := InputAudioSourceFile
	inputAudioDeviceName := ""
	if inputType == InputTypeDesktop {
		if req.DesktopAudioFrom {
			inputAudioSource = InputAudioSourceDevice
			inputAudioDeviceName = strings.TrimSpace(req.DesktopAudioDevice)
		} else if req.DesktopAudioID > 0 {
			audioID = sql.NullInt64{Int64: req.DesktopAudioID, Valid: true}
		}
	} else if inputType == InputTypeUSBCamera || inputType == InputTypeCameraPlus {
		if req.InputDeviceAudioFrom {
			inputAudioSource = InputAudioSourceDevice
			inputAudioDeviceName = strings.TrimSpace(req.InputDeviceAudioDevice)
		} else if req.InputDeviceAudioID > 0 {
			audioID = sql.NullInt64{Int64: req.InputDeviceAudioID, Valid: true}
		}
	} else if req.AudioID > 0 {
		audioID = sql.NullInt64{Int64: req.AudioID, Valid: true}
	}

	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `UPDATE push_settings SET
		model = ?,
		ffmpeg_command = ?,
		is_auto_retry = ?,
		retry_interval = ?,
		is_update = 1,
		input_type = ?,
		output_resolution = ?,
		output_quality = ?,
		output_bitrate_kbps = ?,
		custom_output_params = ?,
		custom_video_codec = ?,
		video_material_id = ?,
		audio_material_id = ?,
		is_mute = ?,
		input_screen = ?,
		input_audio_source = ?,
		input_audio_device_name = ?,
		input_device_name = ?,
		input_device_resolution = ?,
		input_device_framerate = ?,
		input_device_plugins = ?,
		rtsp_url = ?,
		mjpeg_url = ?,
		rtmp_url = ?,
		gb_pull_url = ?,
		onvif_endpoint = ?,
		onvif_username = ?,
		onvif_password = ?,
		onvif_profile_token = ?,
		multi_input_enabled = ?,
		multi_input_layout = ?,
		multi_input_urls = ?,
		multi_input_meta = ?,
		updated_at = ?
	WHERE id = ?`,
		req.Model,
		req.FFmpegCommand,
		boolToInt(req.IsAutoRetry),
		req.RetryInterval,
		inputType,
		req.OutputResolution,
		req.OutputQuality,
		req.OutputBitrateKbps,
		req.CustomOutputParams,
		req.CustomVideoCodec,
		videoID,
		audioID,
		boolToInt(req.IsMute),
		req.InputScreen,
		inputAudioSource,
		inputAudioDeviceName,
		req.InputDeviceName,
		req.InputDeviceResolution,
		req.InputDeviceFramerate,
		req.InputDevicePlugins,
		normalizeInputURL(req.RTSPURL),
		normalizeInputURL(req.MJPEGURL),
		normalizeInputURL(req.RTMPURL),
		normalizeInputURL(req.GBPullURL),
		normalizeInputURL(req.ONVIFEndpoint),
		strings.TrimSpace(req.ONVIFUsername),
		strings.TrimSpace(req.ONVIFPassword),
		strings.TrimSpace(req.ONVIFProfileToken),
		boolToInt(req.MultiInputEnabled),
		strings.TrimSpace(req.MultiInputLayout),
		multiURLsJSON,
		multiMetaJSON,
		now.Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetPushSetting(ctx)
}

func (s *Store) GetLiveSetting(ctx context.Context) (*LiveSetting, error) {
	const q = `SELECT id, area_id, room_name, room_id, content, created_at, updated_at FROM live_settings ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q)
	item := LiveSetting{}
	var createdAt string
	var updatedAt string
	if err := row.Scan(&item.ID, &item.AreaID, &item.RoomName, &item.RoomID, &item.Content, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) UpdateLiveSetting(ctx context.Context, req RoomInfoUpdateRequest) (*LiveSetting, error) {
	current, err := s.GetLiveSetting(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE live_settings SET area_id=?, room_name=?, room_id=?, updated_at=? WHERE id=?`, req.AreaID, req.RoomName, req.RoomID, now, current.ID)
	if err != nil {
		return nil, err
	}
	return s.GetLiveSetting(ctx)
}

func (s *Store) UpdateLiveAnnouncement(ctx context.Context, req RoomNewUpdateRequest) (*LiveSetting, error) {
	current, err := s.GetLiveSetting(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE live_settings SET content=?, room_id=?, updated_at=? WHERE id=?`, req.Content, req.RoomID, now, current.ID)
	if err != nil {
		return nil, err
	}
	return s.GetLiveSetting(ctx)
}

func (s *Store) GetMonitorSetting(ctx context.Context) (*MonitorSetting, error) {
	const q = `SELECT id, is_enabled, room_id, room_url, is_enable_email_notice, smtp_server, smtp_ssl, smtp_port, mail_address, mail_name, password, receivers, created_at, updated_at
	FROM monitor_settings ORDER BY id DESC LIMIT 1`
	row := s.db.QueryRowContext(ctx, q)
	item := MonitorSetting{}
	var isEnabled int
	var isEnableEmail int
	var ssl int
	var createdAt string
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&isEnabled,
		&item.RoomID,
		&item.RoomURL,
		&isEnableEmail,
		&item.SMTPServer,
		&ssl,
		&item.SMTPPort,
		&item.MailAddress,
		&item.MailName,
		&item.Password,
		&item.Receivers,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.IsEnabled = isEnabled == 1
	item.IsEnableEmailNotice = isEnableEmail == 1
	item.SMTPSsl = ssl == 1
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) UpdateMonitorRoomInfo(ctx context.Context, req MonitorRoomInfoUpdateRequest) (*MonitorSetting, error) {
	setting, err := s.GetMonitorSetting(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE monitor_settings SET is_enabled=?, room_id=?, room_url=?, updated_at=? WHERE id=?`, boolToInt(req.IsEnabled), req.RoomID, req.RoomURL, now, setting.ID)
	if err != nil {
		return nil, err
	}
	return s.GetMonitorSetting(ctx)
}

func (s *Store) UpdateMonitorEmailInfo(ctx context.Context, req MonitorEmailUpdateRequest) (*MonitorSetting, error) {
	setting, err := s.GetMonitorSetting(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE monitor_settings SET
		is_enable_email_notice=?,
		smtp_server=?,
		smtp_ssl=?,
		smtp_port=?,
		mail_address=?,
		mail_name=?,
		password=?,
		receivers=?,
		updated_at=?
	WHERE id=?`,
		boolToInt(req.IsEnableEmailNotice),
		req.SMTPServer,
		boolToInt(req.SMTPSsl),
		req.SMTPPort,
		req.MailAddress,
		req.MailName,
		req.Password,
		req.Receivers,
		now,
		setting.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetMonitorSetting(ctx)
}

func (s *Store) GetCookieSetting(ctx context.Context) (*CookieSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, content, refresh_token, created_at, updated_at FROM cookie_settings ORDER BY id DESC LIMIT 1`)
	item := CookieSetting{}
	var createdAt string
	var updatedAt string
	if err := row.Scan(&item.ID, &item.Content, &item.RefreshToken, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) SaveCookieContent(ctx context.Context, content string) error {
	setting, err := s.GetCookieSetting(ctx)
	if err != nil {
		return err
	}
	return s.SaveCookie(ctx, content, setting.RefreshToken)
}

func (s *Store) SaveCookie(ctx context.Context, content string, refreshToken string) error {
	setting, err := s.GetCookieSetting(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE cookie_settings SET content=?, refresh_token=?, updated_at=? WHERE id=?`, content, refreshToken, time.Now().UTC().Format(time.RFC3339Nano), setting.ID)
	return err
}

func (s *Store) SaveRefreshToken(ctx context.Context, refreshToken string) error {
	setting, err := s.GetCookieSetting(ctx)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE cookie_settings SET refresh_token=?, updated_at=? WHERE id=?`, refreshToken, time.Now().UTC().Format(time.RFC3339Nano), setting.ID)
	return err
}

func (s *Store) ListMaterials(ctx context.Context, req MaterialListPageRequest, mediaDir string) (QueryPageModel[MaterialDTO], error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Limit > 100 {
		req.Limit = 100
	}

	filter := "WHERE 1=1"
	args := make([]any, 0, 4)
	if strings.TrimSpace(req.FileName) != "" {
		filter += " AND name LIKE ?"
		args = append(args, "%"+strings.TrimSpace(req.FileName)+"%")
	}
	if req.FileType != FileTypeUnknown {
		filter += " AND file_type = ?"
		args = append(args, req.FileType)
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM materials "+filter, args...).Scan(&total); err != nil {
		return QueryPageModel[MaterialDTO]{}, err
	}

	field := normalizeOrderField(req.Field)
	order := "DESC"
	if strings.EqualFold(req.Order, "asc") {
		order = "ASC"
	}
	offset := (req.Page - 1) * req.Limit
	query := fmt.Sprintf(`SELECT id, name, path, size_kb, file_type, description, media_info, created_at, updated_at FROM materials %s ORDER BY %s %s LIMIT ? OFFSET ?`, filter, field, order)
	args = append(args, req.Limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return QueryPageModel[MaterialDTO]{}, err
	}
	defer rows.Close()

	items := make([]MaterialDTO, 0, req.Limit)
	for rows.Next() {
		var m Material
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&m.ID, &m.Name, &m.Path, &m.SizeKB, &m.FileType, &m.Description, &m.MediaInfo, &createdAt, &updatedAt); err != nil {
			return QueryPageModel[MaterialDTO]{}, err
		}
		fullPath := filepath.Join(mediaDir, filepath.FromSlash(m.Path))
		items = append(items, MaterialDTO{
			ID:          m.ID,
			Name:        m.Name,
			Path:        "~/" + m.Path,
			FullPath:    fullPath,
			Size:        formatKB(m.SizeKB),
			FileType:    fileTypeName(m.FileType),
			Description: m.Description,
			Duration:    "",
			MediaInfo:   m.MediaInfo,
			CreatedTime: parseSQLiteTime(createdAt).Local().Format("2006-01-02 15:04:05"),
		})
	}
	if err := rows.Err(); err != nil {
		return QueryPageModel[MaterialDTO]{}, err
	}
	return QueryPageModel[MaterialDTO]{
		Page:      req.Page,
		PageCount: len(items),
		DataCount: total,
		PageSize:  req.Limit,
		Data:      items,
	}, nil
}

func (s *Store) CreateMaterial(ctx context.Context, material Material) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO materials (name, path, size_kb, file_type, description, media_info, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		material.Name,
		material.Path,
		material.SizeKB,
		material.FileType,
		material.Description,
		material.MediaInfo,
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) GetMaterialByID(ctx context.Context, id int64) (*Material, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, path, size_kb, file_type, description, media_info, created_at, updated_at FROM materials WHERE id=?`, id)
	var m Material
	var createdAt string
	var updatedAt string
	if err := row.Scan(&m.ID, &m.Name, &m.Path, &m.SizeKB, &m.FileType, &m.Description, &m.MediaInfo, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	m.CreatedAt = parseSQLiteTime(createdAt)
	m.UpdatedAt = parseSQLiteTime(updatedAt)
	return &m, nil
}

func (s *Store) DeleteMaterials(ctx context.Context, ids []int64) ([]Material, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			unique[id] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil, nil
	}
	keys := make([]int64, 0, len(unique))
	for id := range unique {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, id := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}

	selectSQL := fmt.Sprintf(`SELECT id, name, path, size_kb, file_type, description, media_info, created_at, updated_at FROM materials WHERE id IN (%s)`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, selectSQL, args...)
	if err != nil {
		return nil, err
	}
	items := make([]Material, 0, len(keys))
	for rows.Next() {
		var m Material
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&m.ID, &m.Name, &m.Path, &m.SizeKB, &m.FileType, &m.Description, &m.MediaInfo, &createdAt, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		m.CreatedAt = parseSQLiteTime(createdAt)
		m.UpdatedAt = parseSQLiteTime(updatedAt)
		items = append(items, m)
	}
	rows.Close()

	deleteSQL := fmt.Sprintf(`DELETE FROM materials WHERE id IN (%s)`, strings.Join(placeholders, ","))
	if _, err := s.db.ExecContext(ctx, deleteSQL, args...); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) SaveDanmakuRule(ctx context.Context, item DanmakuPTZRule) error {
	if strings.TrimSpace(item.Keyword) == "" {
		return errors.New("keyword is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO danmaku_ptz_rules (keyword, action, ptz_direction, ptz_speed, enabled, updated_at)
	VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(keyword) DO UPDATE SET
		action=excluded.action,
		ptz_direction=excluded.ptz_direction,
		ptz_speed=excluded.ptz_speed,
		enabled=excluded.enabled,
		updated_at=excluded.updated_at`,
		strings.TrimSpace(item.Keyword),
		strings.TrimSpace(item.Action),
		strings.TrimSpace(item.PTZDirection),
		item.PTZSpeed,
		boolToInt(item.Enabled),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListDanmakuRules(ctx context.Context, limit int, offset int) ([]DanmakuPTZRule, error) {
	limit = clampLimit(limit, 100, 2000)
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, keyword, action, ptz_direction, ptz_speed, enabled, updated_at
	FROM danmaku_ptz_rules ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]DanmakuPTZRule, 0, limit)
	for rows.Next() {
		var item DanmakuPTZRule
		var enabled int
		var updatedAt string
		if err := rows.Scan(&item.ID, &item.Keyword, &item.Action, &item.PTZDirection, &item.PTZSpeed, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SaveWebhook(ctx context.Context, item WebhookSetting) error {
	if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.URL) == "" {
		return errors.New("name and url are required")
	}
	if _, err := url.ParseRequestURI(item.URL); err != nil {
		return fmt.Errorf("invalid webhook url: %w", err)
	}
	if item.ID > 0 {
		_, err := s.db.ExecContext(ctx, `UPDATE webhook_settings SET name=?, url=?, secret=?, enabled=?, updated_at=? WHERE id=?`,
			item.Name,
			item.URL,
			item.Secret,
			boolToInt(item.Enabled),
			time.Now().UTC().Format(time.RFC3339Nano),
			item.ID,
		)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhook_settings (name, url, secret, enabled, updated_at) VALUES (?, ?, ?, ?, ?)`,
		item.Name,
		item.URL,
		item.Secret,
		boolToInt(item.Enabled),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListWebhooks(ctx context.Context, limit int, offset int) ([]WebhookSetting, error) {
	limit = clampLimit(limit, 100, 2000)
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, url, secret, enabled, updated_at
	FROM webhook_settings ORDER BY id DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]WebhookSetting, 0, limit)
	for rows.Next() {
		var item WebhookSetting
		var enabled int
		var updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.URL, &item.Secret, &enabled, &updatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) CreateWebhookDeliveryLog(ctx context.Context, item WebhookDeliveryLog) error {
	var webhookID sql.NullInt64
	if item.WebhookID != nil && *item.WebhookID > 0 {
		webhookID = sql.NullInt64{Int64: *item.WebhookID, Valid: true}
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhook_delivery_logs (
		webhook_id, webhook_name, event_type, request_body, response_status, response_body,
		success, error_message, duration_ms, attempt, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		webhookID,
		item.WebhookName,
		item.EventType,
		item.RequestBody,
		item.ResponseStatus,
		item.ResponseBody,
		boolToInt(item.Success),
		item.ErrorMessage,
		item.DurationMS,
		item.Attempt,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListWebhookDeliveryLogs(ctx context.Context, limit int, webhookID int64) ([]WebhookDeliveryLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	query := `SELECT id, webhook_id, webhook_name, event_type, request_body, response_status, response_body, success, error_message, duration_ms, attempt, created_at
	FROM webhook_delivery_logs`
	args := make([]any, 0, 2)
	if webhookID > 0 {
		query += ` WHERE webhook_id = ?`
		args = append(args, webhookID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]WebhookDeliveryLog, 0, limit)
	for rows.Next() {
		var item WebhookDeliveryLog
		var maybeWebhookID sql.NullInt64
		var success int
		var createdAt string
		if err := rows.Scan(
			&item.ID,
			&maybeWebhookID,
			&item.WebhookName,
			&item.EventType,
			&item.RequestBody,
			&item.ResponseStatus,
			&item.ResponseBody,
			&success,
			&item.ErrorMessage,
			&item.DurationMS,
			&item.Attempt,
			&createdAt,
		); err != nil {
			return nil, err
		}
		if maybeWebhookID.Valid {
			id := maybeWebhookID.Int64
			item.WebhookID = &id
		}
		item.Success = success == 1
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetBilibiliAPIAlertSetting(ctx context.Context) (*BilibiliAPIAlertSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, enabled, window_minutes, threshold, cooldown_minutes, webhook_event, last_alert_at, updated_at
	FROM bilibili_api_alert_settings ORDER BY id DESC LIMIT 1`)
	item := BilibiliAPIAlertSetting{}
	var enabled int
	var lastAlertAt sql.NullString
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&enabled,
		&item.WindowMinutes,
		&item.Threshold,
		&item.CooldownMinutes,
		&item.WebhookEvent,
		&lastAlertAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.Enabled = enabled == 1
	if lastAlertAt.Valid {
		parsed := parseSQLiteTime(lastAlertAt.String)
		item.LastAlertAt = &parsed
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) SaveBilibiliAPIAlertSetting(ctx context.Context, req BilibiliAPIAlertSetting) (*BilibiliAPIAlertSetting, error) {
	current, err := s.GetBilibiliAPIAlertSetting(ctx)
	if err != nil {
		return nil, err
	}
	if req.WindowMinutes <= 0 {
		req.WindowMinutes = 10
	}
	if req.WindowMinutes > 360 {
		req.WindowMinutes = 360
	}
	if req.Threshold <= 0 {
		req.Threshold = 8
	}
	if req.CooldownMinutes <= 0 {
		req.CooldownMinutes = 15
	}
	if req.CooldownMinutes > 1440 {
		req.CooldownMinutes = 1440
	}
	eventType := strings.TrimSpace(req.WebhookEvent)
	if eventType == "" {
		eventType = "bilibili.api.alert"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE bilibili_api_alert_settings SET
		enabled = ?,
		window_minutes = ?,
		threshold = ?,
		cooldown_minutes = ?,
		webhook_event = ?,
		updated_at = ?
	WHERE id = ?`,
		boolToInt(req.Enabled),
		req.WindowMinutes,
		req.Threshold,
		req.CooldownMinutes,
		eventType,
		now,
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetBilibiliAPIAlertSetting(ctx)
}

func (s *Store) MarkBilibiliAPIAlertSent(ctx context.Context, settingID int64, sentAt time.Time) error {
	if settingID <= 0 {
		return errors.New("invalid setting id")
	}
	if sentAt.IsZero() {
		sentAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE bilibili_api_alert_settings SET last_alert_at = ?, updated_at = ? WHERE id = ?`,
		sentAt.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		settingID,
	)
	return err
}

func (s *Store) CreateBilibiliAPIErrorLog(ctx context.Context, item BilibiliAPIErrorLog) (int64, error) {
	result, err := s.db.ExecContext(ctx, `INSERT INTO bilibili_api_error_logs (
		endpoint, method, stage, http_status, attempt, retryable, request_form, response_headers, response_body, error_message, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Endpoint,
		item.Method,
		item.Stage,
		item.HTTPStatus,
		item.Attempt,
		boolToInt(item.Retryable),
		item.RequestForm,
		item.ResponseHeaders,
		item.ResponseBody,
		item.ErrorMessage,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) ListBilibiliAPIErrorLogs(ctx context.Context, limit int, endpointKeyword string) ([]BilibiliAPIErrorLog, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	query := `SELECT id, endpoint, method, stage, http_status, attempt, retryable, request_form, response_headers, response_body, error_message, created_at
	FROM bilibili_api_error_logs`
	args := make([]any, 0, 2)
	keyword := strings.TrimSpace(endpointKeyword)
	if keyword != "" {
		query += ` WHERE endpoint LIKE ?`
		args = append(args, "%"+keyword+"%")
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]BilibiliAPIErrorLog, 0, limit)
	for rows.Next() {
		var item BilibiliAPIErrorLog
		var retryable int
		var createdAt string
		if err := rows.Scan(
			&item.ID,
			&item.Endpoint,
			&item.Method,
			&item.Stage,
			&item.HTTPStatus,
			&item.Attempt,
			&retryable,
			&item.RequestForm,
			&item.ResponseHeaders,
			&item.ResponseBody,
			&item.ErrorMessage,
			&createdAt,
		); err != nil {
			return nil, err
		}
		item.Retryable = retryable == 1
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetBilibiliAPIErrorLog(ctx context.Context, id int64) (*BilibiliAPIErrorLog, error) {
	if id <= 0 {
		return nil, errors.New("invalid id")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, endpoint, method, stage, http_status, attempt, retryable, request_form, response_headers, response_body, error_message, created_at
	FROM bilibili_api_error_logs WHERE id = ?`, id)
	item := BilibiliAPIErrorLog{}
	var retryable int
	var createdAt string
	if err := row.Scan(
		&item.ID,
		&item.Endpoint,
		&item.Method,
		&item.Stage,
		&item.HTTPStatus,
		&item.Attempt,
		&retryable,
		&item.RequestForm,
		&item.ResponseHeaders,
		&item.ResponseBody,
		&item.ErrorMessage,
		&createdAt,
	); err != nil {
		return nil, err
	}
	item.Retryable = retryable == 1
	item.CreatedAt = parseSQLiteTime(createdAt)
	return &item, nil
}

func (s *Store) GetMaintenanceSetting(ctx context.Context) (*MaintenanceSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, enabled, retention_days, auto_vacuum, last_cleanup_at, last_vacuum_at, updated_at
	FROM maintenance_settings ORDER BY id DESC LIMIT 1`)
	item := MaintenanceSetting{}
	var enabled int
	var autoVacuum int
	var lastCleanupAt sql.NullString
	var lastVacuumAt sql.NullString
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&enabled,
		&item.RetentionDays,
		&autoVacuum,
		&lastCleanupAt,
		&lastVacuumAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.Enabled = enabled == 1
	item.AutoVacuum = autoVacuum == 1
	if lastCleanupAt.Valid {
		parsed := parseSQLiteTime(lastCleanupAt.String)
		item.LastCleanupAt = &parsed
	}
	if lastVacuumAt.Valid {
		parsed := parseSQLiteTime(lastVacuumAt.String)
		item.LastVacuumAt = &parsed
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) SaveMaintenanceSetting(ctx context.Context, req MaintenanceSetting) (*MaintenanceSetting, error) {
	current, err := s.GetMaintenanceSetting(ctx)
	if err != nil {
		return nil, err
	}
	if req.RetentionDays <= 0 {
		req.RetentionDays = 7
	}
	if req.RetentionDays > 3650 {
		req.RetentionDays = 3650
	}
	_, err = s.db.ExecContext(ctx, `UPDATE maintenance_settings SET
		enabled=?,
		retention_days=?,
		auto_vacuum=?,
		updated_at=?
	WHERE id=?`,
		boolToInt(req.Enabled),
		req.RetentionDays,
		boolToInt(req.AutoVacuum),
		time.Now().UTC().Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetMaintenanceSetting(ctx)
}

func (s *Store) MarkMaintenanceCleanup(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE maintenance_settings
	SET last_cleanup_at=?, updated_at=? WHERE id=(SELECT id FROM maintenance_settings ORDER BY id DESC LIMIT 1)`,
		at.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) MarkMaintenanceVacuum(ctx context.Context, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `UPDATE maintenance_settings
	SET last_vacuum_at=?, updated_at=? WHERE id=(SELECT id FROM maintenance_settings ORDER BY id DESC LIMIT 1)`,
		at.UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) CleanupOldDataBefore(ctx context.Context, cutoff time.Time, batchSize int) (CleanupStats, error) {
	if cutoff.IsZero() {
		return CleanupStats{}, errors.New("cutoff is required")
	}
	if batchSize <= 0 {
		batchSize = 500
	}
	if batchSize > 5000 {
		batchSize = 5000
	}

	stats := CleanupStats{}
	var err error
	if stats.LiveEvents, err = s.batchDeleteBefore(ctx, "live_events", "created_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	if stats.DanmakuRecords, err = s.batchDeleteBefore(ctx, "danmaku_records", "created_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	if stats.WebhookDeliveryLogs, err = s.batchDeleteBefore(ctx, "webhook_delivery_logs", "created_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	if stats.BilibiliErrorLogs, err = s.batchDeleteBefore(ctx, "bilibili_api_error_logs", "created_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	if stats.IntegrationTasks, err = s.batchDeleteBefore(ctx, "integration_tasks", "finished_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	// Only clear completed sessions; running sessions have NULL ended_at.
	if stats.StreamSessions, err = s.batchDeleteBefore(ctx, "stream_sessions", "ended_at", cutoff, batchSize); err != nil {
		return CleanupStats{}, err
	}
	stats.Total = stats.LiveEvents + stats.DanmakuRecords + stats.WebhookDeliveryLogs + stats.BilibiliErrorLogs + stats.IntegrationTasks + stats.StreamSessions
	return stats, nil
}

func (s *Store) batchDeleteBefore(ctx context.Context, table string, timeColumn string, cutoff time.Time, batchSize int) (int64, error) {
	total := int64(0)
	cutoffValue := cutoff.UTC().Format(time.RFC3339Nano)
	round := 0
	for {
		if ctx != nil && ctx.Err() != nil {
			return total, ctx.Err()
		}
		query := fmt.Sprintf(`DELETE FROM %s WHERE id IN (
			SELECT id FROM %s WHERE %s IS NOT NULL AND datetime(%s) < datetime(?) LIMIT ?
		)`, table, table, timeColumn, timeColumn)
		result, err := s.db.ExecContext(ctx, query, cutoffValue, batchSize)
		if err != nil {
			return total, err
		}
		affected, affErr := result.RowsAffected()
		if affErr != nil {
			return total, affErr
		}
		total += affected
		if affected < int64(batchSize) {
			break
		}
		round++
		// Yield a short time slice during heavy cleanup to keep the service responsive.
		if round%5 == 0 {
			time.Sleep(15 * time.Millisecond)
		}
	}
	return total, nil
}

func (s *Store) Vacuum(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE);`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM;`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA optimize;`); err != nil {
		return err
	}
	return nil
}

func (s *Store) DBStats(ctx context.Context) (DBStats, error) {
	stats := DBStats{DBPath: s.dbPath}
	if strings.TrimSpace(s.dbPath) == "" {
		return stats, errors.New("empty db path")
	}
	if fileInfo, err := os.Stat(s.dbPath); err == nil {
		stats.DBSizeBytes = fileInfo.Size()
	}
	if fileInfo, err := os.Stat(s.dbPath + "-wal"); err == nil {
		stats.WALSizeBytes = fileInfo.Size()
	}
	if fileInfo, err := os.Stat(s.dbPath + "-shm"); err == nil {
		stats.SHMSizeBytes = fileInfo.Size()
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count;`).Scan(&stats.PageCount); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_size;`).Scan(&stats.PageSize); err != nil {
		return stats, err
	}
	if err := s.db.QueryRowContext(ctx, `PRAGMA freelist_count;`).Scan(&stats.FreeListCount); err != nil {
		return stats, err
	}
	if stats.PageCount > stats.FreeListCount && stats.PageSize > 0 {
		stats.EstimatedInUse = (stats.PageCount - stats.FreeListCount) * stats.PageSize
	}
	return stats, nil
}

func (s *Store) ListAPIKeys(ctx context.Context, limit int, offset int) ([]APIKeySetting, error) {
	limit = clampLimit(limit, 100, 2000)
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, api_key, description, updated_at
	FROM api_key_settings ORDER BY id ASC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]APIKeySetting, 0, limit)
	for rows.Next() {
		var item APIKeySetting
		var updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &item.APIKey, &item.Description, &updatedAt); err != nil {
			return nil, err
		}
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) SaveAPIKey(ctx context.Context, key APIKeySetting) error {
	if strings.TrimSpace(key.Name) == "" {
		return errors.New("name is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_key_settings (name, api_key, description, updated_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(name) DO UPDATE SET
		api_key=excluded.api_key,
		description=excluded.description,
		updated_at=excluded.updated_at`,
		strings.TrimSpace(key.Name),
		key.APIKey,
		key.Description,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) GetAPIKeyByName(ctx context.Context, name string) (*APIKeySetting, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, api_key, description, updated_at
	FROM api_key_settings WHERE name = ? LIMIT 1`, name)
	item := APIKeySetting{}
	var updatedAt string
	if err := row.Scan(&item.ID, &item.Name, &item.APIKey, &item.Description, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) CreateLiveEvent(ctx context.Context, eventType string, payload string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO live_events (event_type, payload, created_at) VALUES (?, ?, ?)`, eventType, payload, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListLiveEvents(ctx context.Context, limit int) ([]LiveEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, event_type, payload, created_at FROM live_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LiveEvent, 0, limit)
	for rows.Next() {
		var item LiveEvent
		var sessionID sql.NullInt64
		var createdAt string
		if err := rows.Scan(&item.ID, &sessionID, &item.EventType, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			id := sessionID.Int64
			item.SessionID = &id
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListLiveEventsByType(ctx context.Context, eventType string, limit int) ([]LiveEvent, error) {
	if strings.TrimSpace(eventType) == "" {
		return nil, errors.New("event type is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, event_type, payload, created_at
	FROM live_events WHERE event_type = ? ORDER BY id DESC LIMIT ?`, strings.TrimSpace(eventType), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LiveEvent, 0, limit)
	for rows.Next() {
		var item LiveEvent
		var sessionID sql.NullInt64
		var createdAt string
		if err := rows.Scan(&item.ID, &sessionID, &item.EventType, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			id := sessionID.Int64
			item.SessionID = &id
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CountLiveEventsByTypeSince(ctx context.Context, eventType string, since time.Time) (int64, error) {
	if strings.TrimSpace(eventType) == "" {
		return 0, errors.New("event type is required")
	}
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM live_events WHERE event_type = ? AND datetime(created_at) >= datetime(?)`,
		strings.TrimSpace(eventType), since.UTC().Format(time.RFC3339Nano)).Scan(&total)
	return total, err
}

func (s *Store) CountLiveEventsSince(ctx context.Context, since time.Time) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM live_events WHERE datetime(created_at) >= datetime(?)`,
		since.UTC().Format(time.RFC3339Nano)).Scan(&total)
	return total, err
}

func (s *Store) ListLiveEventsByTypeSince(ctx context.Context, eventType string, since time.Time, limit int) ([]LiveEvent, error) {
	if strings.TrimSpace(eventType) == "" {
		return nil, errors.New("event type is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 5000 {
		limit = 5000
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, session_id, event_type, payload, created_at
	FROM live_events WHERE event_type = ? AND datetime(created_at) >= datetime(?) ORDER BY id DESC LIMIT ?`,
		strings.TrimSpace(eventType), since.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]LiveEvent, 0, limit)
	for rows.Next() {
		var item LiveEvent
		var sessionID sql.NullInt64
		var createdAt string
		if err := rows.Scan(&item.ID, &sessionID, &item.EventType, &item.Payload, &createdAt); err != nil {
			return nil, err
		}
		if sessionID.Valid {
			id := sessionID.Int64
			item.SessionID = &id
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) InsertDanmakuRecord(ctx context.Context, record DanmakuRecord) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO danmaku_records (room_id, uid, uname, content, raw_payload, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		record.RoomID,
		record.UID,
		record.Uname,
		record.Content,
		record.RawPayload,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) ListDanmakuRecords(ctx context.Context, roomID int64, limit int) ([]DanmakuRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	query := `SELECT id, room_id, uid, uname, content, raw_payload, created_at FROM danmaku_records`
	args := make([]any, 0, 2)
	if roomID > 0 {
		query += ` WHERE room_id = ?`
		args = append(args, roomID)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]DanmakuRecord, 0, limit)
	for rows.Next() {
		var item DanmakuRecord
		var createdAt string
		if err := rows.Scan(&item.ID, &item.RoomID, &item.UID, &item.Uname, &item.Content, &item.RawPayload, &createdAt); err != nil {
			return nil, err
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) CountDanmakuRecordsSince(ctx context.Context, roomID int64, since time.Time) (int64, error) {
	query := `SELECT COUNT(1) FROM danmaku_records WHERE datetime(created_at) >= datetime(?)`
	args := []any{since.UTC().Format(time.RFC3339Nano)}
	if roomID > 0 {
		query += ` AND room_id = ?`
		args = append(args, roomID)
	}
	var total int64
	err := s.db.QueryRowContext(ctx, query, args...).Scan(&total)
	return total, err
}

func (s *Store) ListCameraSources(ctx context.Context, req CameraSourceListRequest) (QueryPageModel[CameraSource], error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	req.Limit = clampLimit(req.Limit, 20, 200)

	filter := "WHERE 1=1"
	args := make([]any, 0, 4)
	keyword := strings.TrimSpace(req.Keyword)
	if keyword != "" {
		filter += ` AND (name LIKE ? OR rtsp_url LIKE ? OR mjpeg_url LIKE ? OR rtmp_url LIKE ? OR gb_pull_url LIKE ? OR gb_device_id LIKE ? OR gb_channel_id LIKE ? OR gb_server LIKE ? OR onvif_endpoint LIKE ? OR usb_device_name LIKE ?)`
		likeValue := "%" + keyword + "%"
		args = append(args, likeValue, likeValue, likeValue, likeValue, likeValue, likeValue, likeValue, likeValue, likeValue, likeValue)
	}
	sourceType := NormalizeCameraSourceType(req.SourceType)
	if sourceType != "" {
		filter += " AND source_type = ?"
		args = append(args, string(sourceType))
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM camera_sources "+filter, args...).Scan(&total); err != nil {
		return QueryPageModel[CameraSource]{}, err
	}

	offset := (req.Page - 1) * req.Limit
	query := `SELECT
		id, name, source_type, rtsp_url, mjpeg_url, rtmp_url, gb_pull_url, gb_device_id, gb_channel_id, gb_server, gb_transport,
		onvif_endpoint, onvif_username, onvif_password, onvif_profile_token,
		usb_device_name, usb_device_resolution, usb_device_framerate,
		description, enabled, created_at, updated_at
	FROM camera_sources ` + filter + ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, req.Limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return QueryPageModel[CameraSource]{}, err
	}
	defer rows.Close()

	items := make([]CameraSource, 0, req.Limit)
	for rows.Next() {
		item, scanErr := scanCameraSource(rows)
		if scanErr != nil {
			return QueryPageModel[CameraSource]{}, scanErr
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return QueryPageModel[CameraSource]{}, err
	}
	return QueryPageModel[CameraSource]{
		Page:      req.Page,
		PageCount: len(items),
		DataCount: total,
		PageSize:  req.Limit,
		Data:      items,
	}, nil
}

func (s *Store) GetCameraSourceByID(ctx context.Context, id int64) (*CameraSource, error) {
	if id <= 0 {
		return nil, errors.New("camera source id must be greater than zero")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, name, source_type, rtsp_url, mjpeg_url, rtmp_url, gb_pull_url, gb_device_id, gb_channel_id, gb_server, gb_transport,
		onvif_endpoint, onvif_username, onvif_password, onvif_profile_token,
		usb_device_name, usb_device_resolution, usb_device_framerate,
		description, enabled, created_at, updated_at
	FROM camera_sources WHERE id=?`, id)
	return scanCameraSource(row)
}

func (s *Store) FindCameraSourceByGBIdentity(ctx context.Context, deviceID string, channelID string) (*CameraSource, error) {
	deviceID = strings.TrimSpace(deviceID)
	channelID = strings.TrimSpace(channelID)
	if deviceID == "" || channelID == "" {
		return nil, errors.New("gb deviceId and channelId are required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, name, source_type, rtsp_url, mjpeg_url, rtmp_url, gb_pull_url, gb_device_id, gb_channel_id, gb_server, gb_transport,
		onvif_endpoint, onvif_username, onvif_password, onvif_profile_token,
		usb_device_name, usb_device_resolution, usb_device_framerate,
		description, enabled, created_at, updated_at
	FROM camera_sources
	WHERE source_type=? AND gb_device_id=? AND gb_channel_id=?
	ORDER BY updated_at DESC, id DESC
	LIMIT 1`,
		CameraSourceTypeGB28181,
		deviceID,
		channelID,
	)
	return scanCameraSource(row)
}

func (s *Store) SaveCameraSource(ctx context.Context, req CameraSourceSaveRequest) (*CameraSource, error) {
	item, err := sanitizeCameraSourceForWrite(req)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if req.ID > 0 {
		if _, getErr := s.GetCameraSourceByID(ctx, req.ID); getErr != nil {
			return nil, getErr
		}
		_, err = s.db.ExecContext(ctx, `UPDATE camera_sources SET
			name = ?,
			source_type = ?,
			rtsp_url = ?,
			mjpeg_url = ?,
			rtmp_url = ?,
			gb_pull_url = ?,
			gb_device_id = ?,
			gb_channel_id = ?,
			gb_server = ?,
			gb_transport = ?,
			onvif_endpoint = ?,
			onvif_username = ?,
			onvif_password = ?,
			onvif_profile_token = ?,
			usb_device_name = ?,
			usb_device_resolution = ?,
			usb_device_framerate = ?,
			description = ?,
			enabled = ?,
			updated_at = ?
		WHERE id = ?`,
			item.Name,
			item.SourceType,
			item.RTSPURL,
			item.MJPEGURL,
			item.RTMPURL,
			item.GBPullURL,
			item.GBDeviceID,
			item.GBChannelID,
			item.GBServer,
			item.GBTransport,
			item.ONVIFEndpoint,
			item.ONVIFUsername,
			item.ONVIFPassword,
			item.ONVIFProfileToken,
			item.USBDeviceName,
			item.USBDeviceResolution,
			item.USBDeviceFramerate,
			item.Description,
			boolToInt(item.Enabled),
			now,
			req.ID,
		)
		if err != nil {
			return nil, err
		}
		return s.GetCameraSourceByID(ctx, req.ID)
	}

	result, err := s.db.ExecContext(ctx, `INSERT INTO camera_sources (
		name, source_type, rtsp_url, mjpeg_url, rtmp_url, gb_pull_url, gb_device_id, gb_channel_id, gb_server, gb_transport,
		onvif_endpoint, onvif_username, onvif_password, onvif_profile_token,
		usb_device_name, usb_device_resolution, usb_device_framerate, description, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.Name,
		item.SourceType,
		item.RTSPURL,
		item.MJPEGURL,
		item.RTMPURL,
		item.GBPullURL,
		item.GBDeviceID,
		item.GBChannelID,
		item.GBServer,
		item.GBTransport,
		item.ONVIFEndpoint,
		item.ONVIFUsername,
		item.ONVIFPassword,
		item.ONVIFProfileToken,
		item.USBDeviceName,
		item.USBDeviceResolution,
		item.USBDeviceFramerate,
		item.Description,
		boolToInt(item.Enabled),
		now,
		now,
	)
	if err != nil {
		return nil, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetCameraSourceByID(ctx, id)
}

func (s *Store) DeleteCameraSources(ctx context.Context, ids []int64) (int64, error) {
	keys := dedupPositiveIDs(ids)
	if len(keys) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys))
	for _, id := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := `DELETE FROM camera_sources WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

func scanCameraSource(scanner interface {
	Scan(dest ...any) error
}) (*CameraSource, error) {
	item := CameraSource{}
	var enabled int
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&item.ID,
		&item.Name,
		&item.SourceType,
		&item.RTSPURL,
		&item.MJPEGURL,
		&item.RTMPURL,
		&item.GBPullURL,
		&item.GBDeviceID,
		&item.GBChannelID,
		&item.GBServer,
		&item.GBTransport,
		&item.ONVIFEndpoint,
		&item.ONVIFUsername,
		&item.ONVIFPassword,
		&item.ONVIFProfileToken,
		&item.USBDeviceName,
		&item.USBDeviceResolution,
		&item.USBDeviceFramerate,
		&item.Description,
		&enabled,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.SourceType = NormalizeCameraSourceType(string(item.SourceType))
	item.Enabled = enabled == 1
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func sanitizeCameraSourceForWrite(req CameraSourceSaveRequest) (*CameraSource, error) {
	sourceType := NormalizeCameraSourceType(req.SourceType)
	if sourceType == "" {
		return nil, errors.New("unsupported camera source type")
	}
	item := &CameraSource{
		Name:                strings.TrimSpace(req.Name),
		SourceType:          sourceType,
		RTSPURL:             normalizeInputURL(req.RTSPURL),
		MJPEGURL:            normalizeInputURL(req.MJPEGURL),
		RTMPURL:             normalizeInputURL(req.RTMPURL),
		GBPullURL:           normalizeInputURL(req.GBPullURL),
		GBDeviceID:          strings.TrimSpace(req.GBDeviceID),
		GBChannelID:         strings.TrimSpace(req.GBChannelID),
		GBServer:            strings.TrimSpace(req.GBServer),
		GBTransport:         strings.TrimSpace(req.GBTransport),
		ONVIFEndpoint:       normalizeInputURL(req.ONVIFEndpoint),
		ONVIFUsername:       strings.TrimSpace(req.ONVIFUsername),
		ONVIFPassword:       strings.TrimSpace(req.ONVIFPassword),
		ONVIFProfileToken:   strings.TrimSpace(req.ONVIFProfileToken),
		USBDeviceName:       strings.TrimSpace(req.USBDeviceName),
		USBDeviceResolution: strings.TrimSpace(req.USBDeviceResolution),
		USBDeviceFramerate:  req.USBDeviceFramerate,
		Description:         strings.TrimSpace(req.Description),
		Enabled:             req.Enabled,
	}
	if item.Name == "" {
		item.Name = buildDefaultCameraName(item)
	}
	if item.USBDeviceResolution == "" {
		item.USBDeviceResolution = "1280x720"
	}
	if item.USBDeviceFramerate <= 0 {
		item.USBDeviceFramerate = 30
	}
	if item.GBTransport == "" {
		item.GBTransport = "udp"
	}

	switch sourceType {
	case CameraSourceTypeRTSP:
		if item.RTSPURL == "" {
			return nil, errors.New("rtsp url is required")
		}
	case CameraSourceTypeMJPEG:
		if item.MJPEGURL == "" {
			return nil, errors.New("mjpeg url is required")
		}
	case CameraSourceTypeONVIF:
		if item.ONVIFEndpoint == "" && item.RTSPURL == "" {
			return nil, errors.New("onvif endpoint or rtsp url is required")
		}
	case CameraSourceTypeUSB:
		if item.USBDeviceName == "" {
			return nil, errors.New("usb device name is required")
		}
	case CameraSourceTypeRTMP:
		if item.RTMPURL == "" {
			return nil, errors.New("rtmp url is required")
		}
	case CameraSourceTypeGB28181:
		if item.GBPullURL == "" {
			return nil, errors.New("gb28181 pull url is required (usually rtsp/http-flv from media server)")
		}
	}
	return item, nil
}

func buildDefaultCameraName(item *CameraSource) string {
	if item == nil {
		return "camera"
	}
	switch item.SourceType {
	case CameraSourceTypeRTSP:
		return "RTSP Camera"
	case CameraSourceTypeMJPEG:
		return "MJPEG Camera"
	case CameraSourceTypeONVIF:
		return "ONVIF Camera"
	case CameraSourceTypeUSB:
		return "USB Camera"
	case CameraSourceTypeRTMP:
		return "RTMP Source"
	case CameraSourceTypeGB28181:
		return "GB28181 Device"
	default:
		return "Camera"
	}
}

func dedupPositiveIDs(ids []int64) []int64 {
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id > 0 {
			unique[id] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil
	}
	keys := make([]int64, 0, len(unique))
	for id := range unique {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func normalizeInputURL(raw string) string {
	return strings.TrimSpace(html.UnescapeString(strings.TrimSpace(raw)))
}

func isLikelyUSBTemplateCommand(command string) bool {
	lower := strings.ToLower(strings.TrimSpace(command))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "-f dshow") || strings.Contains(lower, "video=\"") {
		return true
	}
	if strings.Contains(lower, "-f v4l2") || strings.Contains(lower, "/dev/video") {
		return true
	}
	return false
}

func (s *Store) EnsureMaterialFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return nil
}

func parseJSONStringArray(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{}
	}
	items := make([]string, 0)
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return []string{}
	}
	return normalizeURLList(items, 9)
}

func parseMultiInputMeta(raw string, fallbackURLs []string) []MultiInputSource {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return normalizeMultiInputMeta(nil, fallbackURLs, 9)
	}
	items := make([]MultiInputSource, 0)
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return normalizeMultiInputMeta(nil, fallbackURLs, 9)
	}
	return normalizeMultiInputMeta(items, fallbackURLs, 9)
}

func normalizeURLList(items []string, max int) []string {
	if max <= 0 {
		max = 9
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(items))
	for _, item := range items {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
		if len(result) >= max {
			break
		}
	}
	return result
}

func normalizeMultiInputMeta(items []MultiInputSource, fallbackURLs []string, max int) []MultiInputSource {
	if max <= 0 {
		max = 9
	}
	seen := map[string]struct{}{}
	result := make([]MultiInputSource, 0, len(items))
	primaryFound := false
	appendItem := func(item MultiInputSource) {
		urlValue := strings.TrimSpace(item.URL)
		if urlValue == "" {
			return
		}
		if _, ok := seen[urlValue]; ok {
			return
		}
		seen[urlValue] = struct{}{}
		item.URL = urlValue
		item.Title = strings.TrimSpace(item.Title)
		item.SourceType = strings.TrimSpace(item.SourceType)
		if item.MaterialID < 0 {
			item.MaterialID = 0
		}
		if item.X < 0 {
			item.X = 0
		}
		if item.X > 1 {
			item.X = 1
		}
		if item.Y < 0 {
			item.Y = 0
		}
		if item.Y > 1 {
			item.Y = 1
		}
		if item.W < 0 {
			item.W = 0
		}
		if item.W > 1 {
			item.W = 1
		}
		if item.H < 0 {
			item.H = 0
		}
		if item.H > 1 {
			item.H = 1
		}
		if item.Z < 0 {
			item.Z = 0
		}
		if item.Z > 999 {
			item.Z = 999
		}
		if item.Primary && !primaryFound {
			primaryFound = true
		} else {
			item.Primary = false
		}
		result = append(result, item)
	}

	for _, item := range items {
		appendItem(item)
		if len(result) >= max {
			break
		}
	}
	for _, urlValue := range fallbackURLs {
		appendItem(MultiInputSource{URL: urlValue})
		if len(result) >= max {
			break
		}
	}
	if len(result) > 0 && !primaryFound {
		result[0].Primary = true
	}
	return result
}

func clampLimit(limit int, fallback int, max int) int {
	if fallback <= 0 {
		fallback = 100
	}
	if max <= 0 {
		max = fallback
	}
	if limit <= 0 {
		limit = fallback
	}
	if limit > max {
		limit = max
	}
	return limit
}

func parseSQLiteTime(raw string) time.Time {
	layoutCandidates := []string{time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
	for _, layout := range layoutCandidates {
		if t, err := time.Parse(layout, raw); err == nil {
			return t
		}
	}
	return time.Now().UTC()
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeBitrateKbps(value int) int {
	if value <= 0 {
		return 0
	}
	if value > 120000 {
		return 120000
	}
	return value
}

func normalizeOrderField(field string) string {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "name":
		return "name"
	case "size", "sizekb":
		return "size_kb"
	case "path":
		return "path"
	case "filetype":
		return "file_type"
	case "createdtime", "createdat":
		return "created_at"
	default:
		return "id"
	}
}

func fileTypeName(fileType FileType) string {
	switch fileType {
	case FileTypeVideo:
		return "Video"
	case FileTypeMusic:
		return "Music"
	default:
		return "Unknown"
	}
}

func formatKB(sizeKB int64) string {
	units := []string{"KB", "MB", "GB", "TB"}
	value := float64(sizeKB)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value = value / 1024
		idx++
	}
	return strconv.FormatFloat(value, 'f', 2, 64) + " " + units[idx]
}
