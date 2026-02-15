package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) GetDanmakuConsumerSetting(ctx context.Context) (*DanmakuConsumerSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, enabled, provider, endpoint, auth_token, config_json, poll_interval_sec, batch_size, room_id,
		cursor, last_poll_at, last_error, updated_at
	FROM danmaku_consumer_settings
	ORDER BY id DESC LIMIT 1`)

	item := DanmakuConsumerSetting{}
	var enabled int
	var lastPollAt sql.NullString
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&enabled,
		&item.Provider,
		&item.Endpoint,
		&item.AuthToken,
		&item.ConfigJSON,
		&item.PollIntervalSec,
		&item.BatchSize,
		&item.RoomID,
		&item.Cursor,
		&lastPollAt,
		&item.LastError,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, insertErr := s.db.ExecContext(ctx, `INSERT INTO danmaku_consumer_settings (
				enabled, provider, endpoint, auth_token, config_json, poll_interval_sec, batch_size, room_id, cursor, last_error, updated_at
			) VALUES (0, 'http_polling', '', '', '{}', 3, 20, 0, '', '', ?)`,
				time.Now().UTC().Format(time.RFC3339Nano),
			)
			if insertErr != nil {
				return nil, insertErr
			}
			return s.GetDanmakuConsumerSetting(ctx)
		}
		return nil, err
	}

	item.Enabled = enabled == 1
	if lastPollAt.Valid && strings.TrimSpace(lastPollAt.String) != "" {
		parsed := parseSQLiteTime(lastPollAt.String)
		item.LastPollAt = &parsed
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) GetIntegrationFeatureSetting(ctx context.Context) (*IntegrationFeatureSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, simple_mode, enable_danmaku_consumer, enable_webhook, enable_bot, enable_advanced_stats, enable_task_queue, updated_at
	FROM integration_feature_settings
	ORDER BY id DESC LIMIT 1`)

	item := IntegrationFeatureSetting{}
	var simpleMode int
	var enableDanmakuConsumer int
	var enableWebhook int
	var enableBot int
	var enableAdvancedStats int
	var enableTaskQueue int
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&simpleMode,
		&enableDanmakuConsumer,
		&enableWebhook,
		&enableBot,
		&enableAdvancedStats,
		&enableTaskQueue,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, insertErr := s.db.ExecContext(ctx, `INSERT INTO integration_feature_settings (
				simple_mode, enable_danmaku_consumer, enable_webhook, enable_bot, enable_advanced_stats, enable_task_queue, updated_at
			) VALUES (0, 0, 1, 1, 1, 1, ?)`,
				time.Now().UTC().Format(time.RFC3339Nano),
			)
			if insertErr != nil {
				return nil, insertErr
			}
			return s.GetIntegrationFeatureSetting(ctx)
		}
		return nil, err
	}
	item.SimpleMode = simpleMode == 1
	item.EnableDanmakuConsumer = enableDanmakuConsumer == 1
	item.EnableWebhook = enableWebhook == 1
	item.EnableBot = enableBot == 1
	item.EnableAdvancedStats = enableAdvancedStats == 1
	item.EnableTaskQueue = enableTaskQueue == 1
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) SaveIntegrationFeatureSetting(ctx context.Context, req IntegrationFeatureSetting) (*IntegrationFeatureSetting, error) {
	current, err := s.GetIntegrationFeatureSetting(ctx)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE integration_feature_settings SET
		simple_mode=?,
		enable_danmaku_consumer=?,
		enable_webhook=?,
		enable_bot=?,
		enable_advanced_stats=?,
		enable_task_queue=?,
		updated_at=?
	WHERE id=?`,
		boolToInt(req.SimpleMode),
		boolToInt(req.EnableDanmakuConsumer),
		boolToInt(req.EnableWebhook),
		boolToInt(req.EnableBot),
		boolToInt(req.EnableAdvancedStats),
		boolToInt(req.EnableTaskQueue),
		time.Now().UTC().Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetIntegrationFeatureSetting(ctx)
}

func (s *Store) GetIntegrationQueueSetting(ctx context.Context) (*IntegrationQueueSetting, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		id, webhook_rate_gap_ms, bot_rate_gap_ms, max_workers, lease_interval_ms, updated_at
	FROM integration_queue_settings
	ORDER BY id DESC LIMIT 1`)

	item := IntegrationQueueSetting{}
	var updatedAt string
	if err := row.Scan(
		&item.ID,
		&item.WebhookRateGapMS,
		&item.BotRateGapMS,
		&item.MaxWorkers,
		&item.LeaseIntervalMS,
		&updatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			_, insertErr := s.db.ExecContext(ctx, `INSERT INTO integration_queue_settings (
				webhook_rate_gap_ms, bot_rate_gap_ms, max_workers, lease_interval_ms, updated_at
			) VALUES (300, 300, 3, 500, ?)`,
				time.Now().UTC().Format(time.RFC3339Nano),
			)
			if insertErr != nil {
				return nil, insertErr
			}
			return s.GetIntegrationQueueSetting(ctx)
		}
		return nil, err
	}
	item.UpdatedAt = parseSQLiteTime(updatedAt)
	return &item, nil
}

func (s *Store) SaveIntegrationQueueSetting(ctx context.Context, req IntegrationQueueSetting) (*IntegrationQueueSetting, error) {
	current, err := s.GetIntegrationQueueSetting(ctx)
	if err != nil {
		return nil, err
	}
	req.WebhookRateGapMS = clampInt(req.WebhookRateGapMS, 0, 60000, 300)
	req.BotRateGapMS = clampInt(req.BotRateGapMS, 0, 60000, 300)
	req.MaxWorkers = clampInt(req.MaxWorkers, 1, 16, 3)
	req.LeaseIntervalMS = clampInt(req.LeaseIntervalMS, 100, 5000, 500)
	_, err = s.db.ExecContext(ctx, `UPDATE integration_queue_settings SET
		webhook_rate_gap_ms=?,
		bot_rate_gap_ms=?,
		max_workers=?,
		lease_interval_ms=?,
		updated_at=?
	WHERE id=?`,
		req.WebhookRateGapMS,
		req.BotRateGapMS,
		req.MaxWorkers,
		req.LeaseIntervalMS,
		time.Now().UTC().Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetIntegrationQueueSetting(ctx)
}

func (s *Store) SaveDanmakuConsumerSetting(ctx context.Context, req DanmakuConsumerSetting) (*DanmakuConsumerSetting, error) {
	current, err := s.GetDanmakuConsumerSetting(ctx)
	if err != nil {
		return nil, err
	}

	req.Provider = strings.TrimSpace(req.Provider)
	if req.Provider == "" {
		req.Provider = current.Provider
		if req.Provider == "" {
			req.Provider = "http_polling"
		}
	}
	req.Endpoint = strings.TrimSpace(req.Endpoint)
	req.AuthToken = strings.TrimSpace(req.AuthToken)
	req.ConfigJSON = strings.TrimSpace(req.ConfigJSON)
	if req.ConfigJSON == "" {
		req.ConfigJSON = strings.TrimSpace(current.ConfigJSON)
	}
	if req.ConfigJSON == "" {
		req.ConfigJSON = "{}"
	}
	if req.PollIntervalSec <= 0 {
		req.PollIntervalSec = current.PollIntervalSec
	}
	if req.PollIntervalSec <= 0 {
		req.PollIntervalSec = 3
	}
	if req.PollIntervalSec > 300 {
		req.PollIntervalSec = 300
	}
	if req.BatchSize <= 0 {
		req.BatchSize = current.BatchSize
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 20
	}
	if req.BatchSize > 500 {
		req.BatchSize = 500
	}

	_, err = s.db.ExecContext(ctx, `UPDATE danmaku_consumer_settings SET
		enabled=?,
		provider=?,
		endpoint=?,
		auth_token=?,
		config_json=?,
		poll_interval_sec=?,
		batch_size=?,
		room_id=?,
		cursor=?,
		last_error=?,
		updated_at=?
	WHERE id=?`,
		boolToInt(req.Enabled),
		req.Provider,
		req.Endpoint,
		req.AuthToken,
		req.ConfigJSON,
		req.PollIntervalSec,
		req.BatchSize,
		req.RoomID,
		req.Cursor,
		strings.TrimSpace(req.LastError),
		time.Now().UTC().Format(time.RFC3339Nano),
		current.ID,
	)
	if err != nil {
		return nil, err
	}
	return s.GetDanmakuConsumerSetting(ctx)
}

func (s *Store) UpdateDanmakuConsumerRuntime(ctx context.Context, cursor string, lastErr string, polledAt time.Time) error {
	setting, err := s.GetDanmakuConsumerSetting(ctx)
	if err != nil {
		return err
	}
	if polledAt.IsZero() {
		polledAt = time.Now().UTC()
	}
	_, err = s.db.ExecContext(ctx, `UPDATE danmaku_consumer_settings SET
		cursor=?,
		last_poll_at=?,
		last_error=?,
		updated_at=?
	WHERE id=?`,
		strings.TrimSpace(cursor),
		polledAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(lastErr),
		time.Now().UTC().Format(time.RFC3339Nano),
		setting.ID,
	)
	return err
}

func (s *Store) CreateIntegrationTask(ctx context.Context, item IntegrationTask) (int64, error) {
	item.TaskType = strings.TrimSpace(item.TaskType)
	if item.TaskType == "" {
		return 0, errors.New("taskType is required")
	}
	status := strings.TrimSpace(string(item.Status))
	if status == "" {
		status = string(IntegrationTaskStatusPending)
	}
	item.Priority = clampPriority(item.Priority)
	if item.MaxAttempts <= 0 {
		item.MaxAttempts = 3
	}
	if item.MaxAttempts > 20 {
		item.MaxAttempts = 20
	}
	item.Payload = strings.TrimSpace(item.Payload)
	if item.Payload == "" {
		item.Payload = "{}"
	}
	if item.NextRunAt.IsZero() {
		item.NextRunAt = time.Now().UTC()
	}
	item.RateKey = strings.TrimSpace(item.RateKey)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := s.db.ExecContext(ctx, `INSERT INTO integration_tasks (
		task_type, status, priority, payload, attempt, max_attempts, next_run_at, locked_at, last_error, rate_key, created_at, updated_at, finished_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, NULL)`,
		item.TaskType,
		status,
		item.Priority,
		item.Payload,
		item.Attempt,
		item.MaxAttempts,
		item.NextRunAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(item.LastError),
		item.RateKey,
		now,
		now,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (s *Store) LeaseIntegrationTasks(ctx context.Context, limit int) ([]IntegrationTask, error) {
	limit = clampLimit(limit, 20, 200)
	now := time.Now().UTC()
	ids := make([]int64, 0, limit)

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		rows, queryErr := tx.QueryContext(ctx, `SELECT id FROM integration_tasks
		WHERE status = ? AND datetime(next_run_at) <= datetime(?)
		ORDER BY priority ASC, id ASC LIMIT ?`,
			string(IntegrationTaskStatusPending),
			now.Format(time.RFC3339Nano),
			limit,
		)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()

		for rows.Next() {
			var id int64
			if scanErr := rows.Scan(&id); scanErr != nil {
				return scanErr
			}
			ids = append(ids, id)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}

		placeholders := make([]string, 0, len(ids))
		args := make([]any, 0, len(ids)+4)
		args = append(args,
			string(IntegrationTaskStatusRunning),
			now.Format(time.RFC3339Nano),
			now.Format(time.RFC3339Nano),
			string(IntegrationTaskStatusPending),
		)
		for _, id := range ids {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		updateSQL := fmt.Sprintf(`UPDATE integration_tasks
		SET status = ?, locked_at = ?, updated_at = ?
		WHERE status = ? AND id IN (%s)`, strings.Join(placeholders, ","))
		_, updateErr := tx.ExecContext(ctx, updateSQL, args...)
		return updateErr
	})
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []IntegrationTask{}, nil
	}
	return s.getIntegrationTasksByIDs(ctx, ids)
}

func (s *Store) MarkIntegrationTaskSucceeded(ctx context.Context, id int64, attempt int) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	if attempt < 0 {
		attempt = 0
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		status=?,
		attempt=?,
		locked_at=NULL,
		last_error='',
		updated_at=?,
		finished_at=?
	WHERE id=? AND status=?`,
		string(IntegrationTaskStatusSucceeded),
		attempt,
		now,
		now,
		id,
		string(IntegrationTaskStatusRunning),
	)
	return err
}

func (s *Store) MarkIntegrationTaskRetry(ctx context.Context, id int64, attempt int, nextRunAt time.Time, lastErr string) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	if attempt < 0 {
		attempt = 0
	}
	if nextRunAt.IsZero() {
		nextRunAt = time.Now().UTC().Add(5 * time.Second)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		status=?,
		attempt=?,
		next_run_at=?,
		locked_at=NULL,
		last_error=?,
		updated_at=?,
		finished_at=NULL
	WHERE id=? AND status=?`,
		string(IntegrationTaskStatusPending),
		attempt,
		nextRunAt.UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(lastErr),
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
		string(IntegrationTaskStatusRunning),
	)
	return err
}

func (s *Store) MarkIntegrationTaskDead(ctx context.Context, id int64, attempt int, lastErr string) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	if attempt < 0 {
		attempt = 0
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		status=?,
		attempt=?,
		locked_at=NULL,
		last_error=?,
		updated_at=?,
		finished_at=?
	WHERE id=? AND status=?`,
		string(IntegrationTaskStatusDead),
		attempt,
		strings.TrimSpace(lastErr),
		now,
		now,
		id,
		string(IntegrationTaskStatusRunning),
	)
	return err
}

func (s *Store) CancelIntegrationTask(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		status=?,
		locked_at=NULL,
		updated_at=?,
		finished_at=?
	WHERE id=? AND status IN (?, ?)`,
		string(IntegrationTaskStatusCancelled),
		now,
		now,
		id,
		string(IntegrationTaskStatusPending),
		string(IntegrationTaskStatusRunning),
	)
	return err
}

func (s *Store) UpdateIntegrationTaskPriority(ctx context.Context, id int64, priority int) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	priority = clampPriority(priority)
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		priority=?,
		updated_at=?
	WHERE id=? AND status IN (?, ?, ?)`,
		priority,
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
		string(IntegrationTaskStatusPending),
		string(IntegrationTaskStatusDead),
		string(IntegrationTaskStatusCancelled),
	)
	return err
}

func (s *Store) RetryIntegrationTask(ctx context.Context, id int64) error {
	if id <= 0 {
		return errors.New("invalid task id")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE integration_tasks SET
		status=?,
		attempt=0,
		next_run_at=?,
		locked_at=NULL,
		last_error='',
		updated_at=?,
		finished_at=NULL
	WHERE id=? AND status IN (?, ?)`,
		string(IntegrationTaskStatusPending),
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		id,
		string(IntegrationTaskStatusDead),
		string(IntegrationTaskStatusCancelled),
	)
	return err
}

func (s *Store) RetryIntegrationTasksBatch(ctx context.Context, ids []int64, status string, taskType string, limit int) (int64, error) {
	limit = clampLimit(limit, 100, 5000)
	status = strings.TrimSpace(status)
	taskType = strings.TrimSpace(taskType)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	if len(ids) > 0 {
		cleanIDs := make([]int64, 0, len(ids))
		seen := map[int64]struct{}{}
		for _, id := range ids {
			if id <= 0 {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			cleanIDs = append(cleanIDs, id)
		}
		if len(cleanIDs) == 0 {
			return 0, nil
		}
		placeholders := make([]string, 0, len(cleanIDs))
		args := make([]any, 0, len(cleanIDs)+7)
		args = append(args,
			string(IntegrationTaskStatusPending),
			now,
			now,
			string(IntegrationTaskStatusDead),
			string(IntegrationTaskStatusCancelled),
		)
		for _, id := range cleanIDs {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query := fmt.Sprintf(`UPDATE integration_tasks SET
			status=?,
			attempt=0,
			next_run_at=?,
			locked_at=NULL,
			last_error='',
			updated_at=?,
			finished_at=NULL
		WHERE status IN (?, ?) AND id IN (%s)`, strings.Join(placeholders, ","))
		result, err := s.db.ExecContext(ctx, query, args...)
		if err != nil {
			return 0, err
		}
		return result.RowsAffected()
	}

	query := `UPDATE integration_tasks SET
		status=?,
		attempt=0,
		next_run_at=?,
		locked_at=NULL,
		last_error='',
		updated_at=?,
		finished_at=NULL
	WHERE id IN (
		SELECT id FROM integration_tasks
		WHERE status IN (?, ?)`
	args := []any{
		string(IntegrationTaskStatusPending),
		now,
		now,
		string(IntegrationTaskStatusDead),
		string(IntegrationTaskStatusCancelled),
	}
	if status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if taskType != "" {
		query += ` AND task_type = ?`
		args = append(args, taskType)
	}
	query += ` ORDER BY id ASC LIMIT ?)`
	args = append(args, limit)

	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ListIntegrationTasks(ctx context.Context, limit int, status string, taskType string) ([]IntegrationTask, error) {
	limit = clampLimit(limit, 100, 2000)
	status = strings.TrimSpace(status)
	taskType = strings.TrimSpace(taskType)

	query := `SELECT id, task_type, status, priority, payload, attempt, max_attempts, next_run_at, locked_at, last_error, rate_key, created_at, updated_at, finished_at
	FROM integration_tasks`
	args := make([]any, 0, 4)
	conditions := make([]string, 0, 2)
	if status != "" {
		conditions = append(conditions, "status = ?")
		args = append(args, status)
	}
	if taskType != "" {
		conditions = append(conditions, "task_type = ?")
		args = append(args, taskType)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntegrationTaskRows(rows)
}

func (s *Store) IntegrationTaskSummary(ctx context.Context) (IntegrationTaskSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT status, COUNT(1) FROM integration_tasks GROUP BY status`)
	if err != nil {
		return IntegrationTaskSummary{}, err
	}
	defer rows.Close()
	summary := IntegrationTaskSummary{}
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return IntegrationTaskSummary{}, err
		}
		switch IntegrationTaskStatus(strings.TrimSpace(status)) {
		case IntegrationTaskStatusPending:
			summary.Pending = count
		case IntegrationTaskStatusRunning:
			summary.Running = count
		case IntegrationTaskStatusSucceeded:
			summary.Succeeded = count
		case IntegrationTaskStatusDead:
			summary.Dead = count
		case IntegrationTaskStatusCancelled:
			summary.Cancelled = count
		}
	}
	return summary, rows.Err()
}

func (s *Store) getIntegrationTasksByIDs(ctx context.Context, ids []int64) ([]IntegrationTask, error) {
	if len(ids) == 0 {
		return []IntegrationTask{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	query := fmt.Sprintf(`SELECT id, task_type, status, priority, payload, attempt, max_attempts, next_run_at, locked_at, last_error, rate_key, created_at, updated_at, finished_at
	FROM integration_tasks WHERE id IN (%s) ORDER BY priority ASC, id ASC`, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIntegrationTaskRows(rows)
}

func scanIntegrationTaskRows(rows *sql.Rows) ([]IntegrationTask, error) {
	items := make([]IntegrationTask, 0, 64)
	for rows.Next() {
		var item IntegrationTask
		var status string
		var nextRunAt string
		var lockedAt sql.NullString
		var createdAt string
		var updatedAt string
		var finishedAt sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.TaskType,
			&status,
			&item.Priority,
			&item.Payload,
			&item.Attempt,
			&item.MaxAttempts,
			&nextRunAt,
			&lockedAt,
			&item.LastError,
			&item.RateKey,
			&createdAt,
			&updatedAt,
			&finishedAt,
		); err != nil {
			return nil, err
		}
		item.Status = IntegrationTaskStatus(strings.TrimSpace(status))
		item.NextRunAt = parseSQLiteTime(nextRunAt)
		if lockedAt.Valid && strings.TrimSpace(lockedAt.String) != "" {
			parsed := parseSQLiteTime(lockedAt.String)
			item.LockedAt = &parsed
		}
		item.CreatedAt = parseSQLiteTime(createdAt)
		item.UpdatedAt = parseSQLiteTime(updatedAt)
		if finishedAt.Valid && strings.TrimSpace(finishedAt.String) != "" {
			parsed := parseSQLiteTime(finishedAt.String)
			item.FinishedAt = &parsed
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func clampPriority(priority int) int {
	if priority <= 0 {
		return 100
	}
	if priority > 1000 {
		return 1000
	}
	return priority
}

func clampInt(value int, min int, max int, fallback int) int {
	if value == 0 {
		value = fallback
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
