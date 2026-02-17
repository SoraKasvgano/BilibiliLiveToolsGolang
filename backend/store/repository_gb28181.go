package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Store) ListGB28181Devices(ctx context.Context, req GB28181DeviceListRequest) (QueryPageModel[GB28181Device], error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	req.Limit = clampLimit(req.Limit, 20, 200)

	filter := "WHERE 1=1"
	args := make([]any, 0, 8)
	if keyword := strings.TrimSpace(req.Keyword); keyword != "" {
		filter += " AND (device_id LIKE ? OR name LIKE ? OR remote_addr LIKE ?)"
		likeValue := "%" + keyword + "%"
		args = append(args, likeValue, likeValue, likeValue)
	}
	status := normalizeGB28181DeviceStatus(req.Status)
	if status != "" {
		filter += " AND status = ?"
		args = append(args, string(status))
	}

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM gb28181_devices "+filter, args...).Scan(&total); err != nil {
		return QueryPageModel[GB28181Device]{}, err
	}

	offset := (req.Page - 1) * req.Limit
	query := `SELECT
		id, device_id, name, auth_password, transport, remote_addr, status, expires,
		last_register_at, last_keepalive_at, manufacturer, model, firmware, raw_payload,
		created_at, updated_at
	FROM gb28181_devices ` + filter + ` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, req.Limit, offset)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return QueryPageModel[GB28181Device]{}, err
	}
	defer rows.Close()

	items := make([]GB28181Device, 0, req.Limit)
	for rows.Next() {
		item, scanErr := scanGB28181Device(rows)
		if scanErr != nil {
			return QueryPageModel[GB28181Device]{}, scanErr
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return QueryPageModel[GB28181Device]{}, err
	}
	return QueryPageModel[GB28181Device]{
		Page:      req.Page,
		PageCount: len(items),
		DataCount: total,
		PageSize:  req.Limit,
		Data:      items,
	}, nil
}

func (s *Store) GetGB28181DeviceByID(ctx context.Context, id int64) (*GB28181Device, error) {
	if id <= 0 {
		return nil, errors.New("gb28181 device id must be greater than zero")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, device_id, name, auth_password, transport, remote_addr, status, expires,
		last_register_at, last_keepalive_at, manufacturer, model, firmware, raw_payload,
		created_at, updated_at
	FROM gb28181_devices WHERE id=?`, id)
	return scanGB28181Device(row)
}

func (s *Store) GetGB28181DeviceByDeviceID(ctx context.Context, deviceID string) (*GB28181Device, error) {
	deviceID = strings.TrimSpace(deviceID)
	if deviceID == "" {
		return nil, errors.New("device id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, device_id, name, auth_password, transport, remote_addr, status, expires,
		last_register_at, last_keepalive_at, manufacturer, model, firmware, raw_payload,
		created_at, updated_at
	FROM gb28181_devices WHERE device_id=?`, deviceID)
	return scanGB28181Device(row)
}

func (s *Store) SaveGB28181Device(ctx context.Context, req GB28181DeviceSaveRequest) (*GB28181Device, error) {
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		return nil, errors.New("deviceId is required")
	}
	transport := normalizeGBTransport(req.Transport)
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deviceID
	}
	status := GB28181DeviceStatusUnknown
	if !req.Enabled {
		status = GB28181DeviceStatusOffline
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	if req.ID > 0 {
		if _, err := s.GetGB28181DeviceByID(ctx, req.ID); err != nil {
			return nil, err
		}
		_, err := s.db.ExecContext(ctx, `UPDATE gb28181_devices SET
			device_id=?, name=?, auth_password=?, transport=?, remote_addr=?, status=?, updated_at=?
		WHERE id=?`,
			deviceID,
			name,
			strings.TrimSpace(req.AuthPassword),
			transport,
			strings.TrimSpace(req.RemoteAddr),
			status,
			now,
			req.ID,
		)
		if err != nil {
			return nil, err
		}
		return s.GetGB28181DeviceByID(ctx, req.ID)
	}

	result, err := s.db.ExecContext(ctx, `INSERT INTO gb28181_devices (
		device_id, name, auth_password, transport, remote_addr, status, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		deviceID,
		name,
		strings.TrimSpace(req.AuthPassword),
		transport,
		strings.TrimSpace(req.RemoteAddr),
		status,
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
	return s.GetGB28181DeviceByID(ctx, id)
}

func (s *Store) DeleteGB28181Devices(ctx context.Context, ids []int64) (int64, error) {
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
	query := `DELETE FROM gb28181_devices WHERE id IN (` + strings.Join(placeholders, ",") + `)`
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

func (s *Store) BatchUpdateGB28181DeviceStatus(ctx context.Context, ids []int64, status GB28181DeviceStatus) (int64, error) {
	keys := dedupPositiveIDs(ids)
	if len(keys) == 0 {
		return 0, nil
	}
	placeholders := make([]string, 0, len(keys))
	args := make([]any, 0, len(keys)+2)
	for _, id := range keys {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	args = append([]any{normalizeGB28181DeviceStatus(string(status)), now}, args...)
	query := `UPDATE gb28181_devices SET status=?, updated_at=? WHERE id IN (` + strings.Join(placeholders, ",") + `)`
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

func (s *Store) UpsertGB28181RuntimeDevice(ctx context.Context, req GB28181RuntimeDeviceUpsertRequest) (*GB28181Device, error) {
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" {
		return nil, errors.New("device id is required")
	}
	transport := normalizeGBTransport(req.Transport)
	status := req.Status
	if status == "" {
		status = GB28181DeviceStatusUnknown
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = deviceID
	}
	expires := req.Expires
	if expires <= 0 {
		expires = 3600
	}

	current, err := s.GetGB28181DeviceByDeviceID(ctx, deviceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	now := time.Now().UTC()
	if current == nil {
		lastRegisterAt := sql.NullString{}
		lastKeepaliveAt := sql.NullString{}
		if req.LastRegisterAt != nil {
			lastRegisterAt = sql.NullString{String: req.LastRegisterAt.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		if req.LastKeepaliveAt != nil {
			lastKeepaliveAt = sql.NullString{String: req.LastKeepaliveAt.UTC().Format(time.RFC3339Nano), Valid: true}
		}
		result, insertErr := s.db.ExecContext(ctx, `INSERT INTO gb28181_devices (
			device_id, name, auth_password, transport, remote_addr, status, expires,
			last_register_at, last_keepalive_at, manufacturer, model, firmware, raw_payload, created_at, updated_at
		) VALUES (?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			deviceID,
			name,
			transport,
			strings.TrimSpace(req.RemoteAddr),
			status,
			expires,
			lastRegisterAt,
			lastKeepaliveAt,
			strings.TrimSpace(req.Manufacturer),
			strings.TrimSpace(req.Model),
			strings.TrimSpace(req.Firmware),
			strings.TrimSpace(req.RawPayload),
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
		)
		if insertErr != nil {
			return nil, insertErr
		}
		id, idErr := result.LastInsertId()
		if idErr != nil {
			return nil, idErr
		}
		return s.GetGB28181DeviceByID(ctx, id)
	}

	lastRegister := sql.NullString{}
	if req.LastRegisterAt != nil {
		lastRegister = sql.NullString{String: req.LastRegisterAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	lastKeepalive := sql.NullString{}
	if req.LastKeepaliveAt != nil {
		lastKeepalive = sql.NullString{String: req.LastKeepaliveAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE gb28181_devices SET
		name = COALESCE(NULLIF(?, ''), name),
		transport = ?,
		remote_addr = ?,
		status = ?,
		expires = ?,
		last_register_at = COALESCE(?, last_register_at),
		last_keepalive_at = COALESCE(?, last_keepalive_at),
		manufacturer = COALESCE(NULLIF(?, ''), manufacturer),
		model = COALESCE(NULLIF(?, ''), model),
		firmware = COALESCE(NULLIF(?, ''), firmware),
		raw_payload = COALESCE(NULLIF(?, ''), raw_payload),
		updated_at = ?
	WHERE id = ?`,
		name,
		transport,
		strings.TrimSpace(req.RemoteAddr),
		status,
		expires,
		lastRegister,
		lastKeepalive,
		strings.TrimSpace(req.Manufacturer),
		strings.TrimSpace(req.Model),
		strings.TrimSpace(req.Firmware),
		strings.TrimSpace(req.RawPayload),
		now.Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetGB28181DeviceByID(ctx, current.ID)
}

func (s *Store) TouchGB28181DeviceKeepalive(ctx context.Context, deviceID string, remoteAddr string, rawPayload string) error {
	current, err := s.GetGB28181DeviceByDeviceID(ctx, deviceID)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `UPDATE gb28181_devices SET
		status=?,
		remote_addr=COALESCE(NULLIF(?, ''), remote_addr),
		last_keepalive_at=?,
		raw_payload=COALESCE(NULLIF(?, ''), raw_payload),
		updated_at=?
	WHERE id=?`,
		GB28181DeviceStatusOnline,
		strings.TrimSpace(remoteAddr),
		now,
		strings.TrimSpace(rawPayload),
		now,
		current.ID,
	)
	return err
}

func (s *Store) ReplaceGB28181ChannelsByDeviceID(ctx context.Context, deviceID string, channels []GB28181ChannelUpsertRequest) error {
	device, err := s.GetGB28181DeviceByDeviceID(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	seen := map[string]struct{}{}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM gb28181_channels WHERE device_row_id=?`, device.ID); err != nil {
			return err
		}
		for _, ch := range channels {
			channelID := strings.TrimSpace(ch.ChannelID)
			if channelID == "" {
				continue
			}
			if _, ok := seen[channelID]; ok {
				continue
			}
			seen[channelID] = struct{}{}
			if _, err := tx.ExecContext(ctx, `INSERT INTO gb28181_channels (
				device_row_id, device_id, channel_id, name, manufacturer, model, owner, civil_code, address,
				parental, parent_id, safety_way, register_way, secrecy, status, longitude, latitude, raw_payload, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				device.ID,
				strings.TrimSpace(ch.DeviceID),
				channelID,
				strings.TrimSpace(ch.Name),
				strings.TrimSpace(ch.Manufacturer),
				strings.TrimSpace(ch.Model),
				strings.TrimSpace(ch.Owner),
				strings.TrimSpace(ch.CivilCode),
				strings.TrimSpace(ch.Address),
				ch.Parental,
				strings.TrimSpace(ch.ParentID),
				ch.SafetyWay,
				ch.RegisterWay,
				ch.Secrecy,
				strings.TrimSpace(ch.Status),
				strings.TrimSpace(ch.Longitude),
				strings.TrimSpace(ch.Latitude),
				strings.TrimSpace(ch.RawPayload),
				now,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) ListGB28181ChannelsByDeviceID(ctx context.Context, deviceID string, page int, limit int) (QueryPageModel[GB28181Channel], error) {
	device, err := s.GetGB28181DeviceByDeviceID(ctx, strings.TrimSpace(deviceID))
	if err != nil {
		return QueryPageModel[GB28181Channel]{}, err
	}
	return s.ListGB28181ChannelsByDeviceRowID(ctx, device.ID, page, limit)
}

func (s *Store) ListGB28181ChannelsByDeviceRowID(ctx context.Context, deviceRowID int64, page int, limit int) (QueryPageModel[GB28181Channel], error) {
	if deviceRowID <= 0 {
		return QueryPageModel[GB28181Channel]{}, errors.New("device row id is required")
	}
	if page <= 0 {
		page = 1
	}
	limit = clampLimit(limit, 50, 500)

	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM gb28181_channels WHERE device_row_id=?", deviceRowID).Scan(&total); err != nil {
		return QueryPageModel[GB28181Channel]{}, err
	}
	offset := (page - 1) * limit
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, device_row_id, device_id, channel_id, name, manufacturer, model, owner, civil_code, address,
		parental, parent_id, safety_way, register_way, secrecy, status, longitude, latitude, raw_payload, updated_at
	FROM gb28181_channels WHERE device_row_id=? ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, deviceRowID, limit, offset)
	if err != nil {
		return QueryPageModel[GB28181Channel]{}, err
	}
	defer rows.Close()

	items := make([]GB28181Channel, 0, limit)
	for rows.Next() {
		item, scanErr := scanGB28181Channel(rows)
		if scanErr != nil {
			return QueryPageModel[GB28181Channel]{}, scanErr
		}
		items = append(items, *item)
	}
	if err := rows.Err(); err != nil {
		return QueryPageModel[GB28181Channel]{}, err
	}
	return QueryPageModel[GB28181Channel]{
		Page:      page,
		PageCount: len(items),
		DataCount: total,
		PageSize:  limit,
		Data:      items,
	}, nil
}

func (s *Store) UpsertGB28181Session(ctx context.Context, req GB28181SessionUpsertRequest) (*GB28181Session, error) {
	callID := strings.TrimSpace(req.CallID)
	if callID == "" {
		return nil, errors.New("call id is required")
	}
	status := req.Status
	if status == "" {
		status = GB28181SessionStatusInviting
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	existing, err := s.GetGB28181SessionByCallID(ctx, callID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	deviceID := strings.TrimSpace(req.DeviceID)
	if deviceID == "" && existing != nil {
		deviceID = strings.TrimSpace(existing.DeviceID)
	}
	if deviceID == "" {
		return nil, errors.New("device id is required")
	}
	device, deviceErr := s.GetGB28181DeviceByDeviceID(ctx, deviceID)
	if deviceErr != nil && existing == nil {
		return nil, deviceErr
	}
	channelID := strings.TrimSpace(req.ChannelID)
	if channelID == "" && existing != nil {
		channelID = strings.TrimSpace(existing.ChannelID)
	}
	if existing == nil {
		if device == nil {
			return nil, errors.New("device not found")
		}
		result, insertErr := s.db.ExecContext(ctx, `INSERT INTO gb28181_sessions (
			device_row_id, device_id, channel_id, call_id, branch, stream_id, remote_addr, status, sdp_body, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			device.ID,
			device.DeviceID,
			channelID,
			callID,
			strings.TrimSpace(req.Branch),
			strings.TrimSpace(req.StreamID),
			strings.TrimSpace(req.RemoteAddr),
			status,
			strings.TrimSpace(req.SDPBody),
			now,
			now,
		)
		if insertErr != nil {
			return nil, insertErr
		}
		id, idErr := result.LastInsertId()
		if idErr != nil {
			return nil, idErr
		}
		return s.GetGB28181SessionByID(ctx, id)
	}

	deviceRowID := existing.DeviceRowID
	persistDeviceID := existing.DeviceID
	if device != nil {
		deviceRowID = device.ID
		persistDeviceID = device.DeviceID
	}
	endedAt := sql.NullString{}
	if status == GB28181SessionStatusTerminated || status == GB28181SessionStatusFailed {
		endedAt = sql.NullString{String: now, Valid: true}
	}
	_, err = s.db.ExecContext(ctx, `UPDATE gb28181_sessions SET
		device_row_id=?,
		device_id=?,
		channel_id=?,
		branch=COALESCE(NULLIF(?, ''), branch),
		stream_id=COALESCE(NULLIF(?, ''), stream_id),
		remote_addr=COALESCE(NULLIF(?, ''), remote_addr),
		status=?,
		sdp_body=COALESCE(NULLIF(?, ''), sdp_body),
		ended_at=COALESCE(?, ended_at),
		updated_at=?
	WHERE id=?`,
		deviceRowID,
		persistDeviceID,
		channelID,
		strings.TrimSpace(req.Branch),
		strings.TrimSpace(req.StreamID),
		strings.TrimSpace(req.RemoteAddr),
		status,
		strings.TrimSpace(req.SDPBody),
		endedAt,
		now,
		existing.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetGB28181SessionByID(ctx, existing.ID)
}

func (s *Store) GetGB28181SessionByCallID(ctx context.Context, callID string) (*GB28181Session, error) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil, errors.New("call id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, device_row_id, device_id, channel_id, call_id, branch, stream_id, remote_addr, status, sdp_body,
		created_at, updated_at, ended_at
	FROM gb28181_sessions WHERE call_id=?`, callID)
	return scanGB28181Session(row)
}

func (s *Store) GetGB28181SessionByID(ctx context.Context, id int64) (*GB28181Session, error) {
	if id <= 0 {
		return nil, errors.New("session id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		id, device_row_id, device_id, channel_id, call_id, branch, stream_id, remote_addr, status, sdp_body,
		created_at, updated_at, ended_at
	FROM gb28181_sessions WHERE id=?`, id)
	return scanGB28181Session(row)
}

func (s *Store) ListGB28181Sessions(ctx context.Context, limit int, status string) ([]GB28181Session, error) {
	limit = clampLimit(limit, 100, 1000)
	filter := "WHERE 1=1"
	args := make([]any, 0, 2)
	if strings.TrimSpace(status) != "" {
		filter += " AND status = ?"
		args = append(args, strings.TrimSpace(status))
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, device_row_id, device_id, channel_id, call_id, branch, stream_id, remote_addr, status, sdp_body,
		created_at, updated_at, ended_at
	FROM gb28181_sessions `+filter+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]GB28181Session, 0, limit)
	for rows.Next() {
		item, scanErr := scanGB28181Session(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, *item)
	}
	return items, rows.Err()
}

func (s *Store) MarkGB28181InvitingSessionsTimeout(ctx context.Context, before time.Time) (int64, error) {
	at := before.UTC().Format(time.RFC3339Nano)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `UPDATE gb28181_sessions
SET status=?, ended_at=COALESCE(ended_at, ?), updated_at=?
WHERE status=? AND updated_at <= ?`,
		GB28181SessionStatusFailed,
		now,
		now,
		GB28181SessionStatusInviting,
		at,
	)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}

func scanGB28181Device(scanner interface{ Scan(dest ...any) error }) (*GB28181Device, error) {
	item := GB28181Device{}
	var status string
	var lastRegister sql.NullString
	var lastKeepalive sql.NullString
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&item.ID,
		&item.DeviceID,
		&item.Name,
		&item.AuthPassword,
		&item.Transport,
		&item.RemoteAddr,
		&status,
		&item.Expires,
		&lastRegister,
		&lastKeepalive,
		&item.Manufacturer,
		&item.Model,
		&item.Firmware,
		&item.RawPayload,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.Status = normalizeGB28181DeviceStatus(status)
	if lastRegister.Valid {
		value := parseSQLiteTime(lastRegister.String)
		item.LastRegisterAt = &value
	}
	if lastKeepalive.Valid {
		value := parseSQLiteTime(lastKeepalive.String)
		item.LastKeepaliveAt = &value
	}
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	item.Transport = normalizeGBTransport(item.Transport)
	return &item, nil
}

func scanGB28181Channel(scanner interface{ Scan(dest ...any) error }) (*GB28181Channel, error) {
	item := GB28181Channel{}
	var updatedAt string
	if err := scanner.Scan(
		&item.ID,
		&item.DeviceRowID,
		&item.DeviceID,
		&item.ChannelID,
		&item.Name,
		&item.Manufacturer,
		&item.Model,
		&item.Owner,
		&item.CivilCode,
		&item.Address,
		&item.Parental,
		&item.ParentID,
		&item.SafetyWay,
		&item.RegisterWay,
		&item.Secrecy,
		&item.Status,
		&item.Longitude,
		&item.Latitude,
		&item.RawPayload,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func scanGB28181Session(scanner interface{ Scan(dest ...any) error }) (*GB28181Session, error) {
	item := GB28181Session{}
	var status string
	var createdAt string
	var updatedAt string
	var endedAt sql.NullString
	if err := scanner.Scan(
		&item.ID,
		&item.DeviceRowID,
		&item.DeviceID,
		&item.ChannelID,
		&item.CallID,
		&item.Branch,
		&item.StreamID,
		&item.RemoteAddr,
		&status,
		&item.SDPBody,
		&createdAt,
		&updatedAt,
		&endedAt,
	); err != nil {
		return nil, err
	}
	item.Status = normalizeGB28181SessionStatus(status)
	item.CreatedAt = parseSQLiteTime(createdAt)
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	if endedAt.Valid {
		value := parseSQLiteTime(endedAt.String)
		item.EndedAt = &value
	}
	return &item, nil
}

func normalizeGBTransport(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "udp", "tcp", "both":
		return value
	default:
		return "udp"
	}
}

func normalizeGB28181DeviceStatus(raw string) GB28181DeviceStatus {
	switch GB28181DeviceStatus(strings.ToLower(strings.TrimSpace(raw))) {
	case GB28181DeviceStatusOnline:
		return GB28181DeviceStatusOnline
	case GB28181DeviceStatusOffline:
		return GB28181DeviceStatusOffline
	default:
		return GB28181DeviceStatusUnknown
	}
}

func normalizeGB28181SessionStatus(raw string) GB28181SessionStatus {
	switch GB28181SessionStatus(strings.ToLower(strings.TrimSpace(raw))) {
	case GB28181SessionStatusInviting:
		return GB28181SessionStatusInviting
	case GB28181SessionStatusEstablished:
		return GB28181SessionStatusEstablished
	case GB28181SessionStatusTerminated:
		return GB28181SessionStatusTerminated
	case GB28181SessionStatusFailed:
		return GB28181SessionStatusFailed
	default:
		return GB28181SessionStatusInviting
	}
}
