package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"bilibililivetools/gover/backend/store"
)

func (s *Service) BuildAdvancedStats(ctx context.Context, hours int, granularity string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if enabled, err := s.IsFeatureEnabled(ctx, FeatureAdvancedStats); err != nil || !enabled {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("feature is disabled: advanced_stats")
	}
	if hours <= 0 {
		hours = 24
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	granularity = normalizeStatsGranularity(granularity)
	now := time.Now().UTC()
	cutoff := now.Add(-time.Duration(hours) * time.Hour)
	cutoffValue := cutoff.Format(time.RFC3339Nano)
	db := s.store.DB()

	eventCount, err := s.store.CountLiveEventsSince(ctx, cutoff)
	if err != nil {
		return nil, err
	}
	danmakuCount, err := s.store.CountDanmakuRecordsSince(ctx, 0, cutoff)
	if err != nil {
		return nil, err
	}
	ruleExecuted, _ := s.store.CountLiveEventsByTypeSince(ctx, "danmaku.rule.executed", cutoff)
	ruleErrors, _ := s.store.CountLiveEventsByTypeSince(ctx, "danmaku.rule.error", cutoff)
	botCommands, _ := s.store.CountLiveEventsByTypeSince(ctx, "bot.command", cutoff)
	alertCount, _ := s.store.CountLiveEventsByTypeSince(ctx, "bilibili.api.alert.sent", cutoff)

	eventTypeTop, err := queryEventTypeTop(ctx, db, cutoffValue, 30)
	if err != nil {
		return nil, err
	}
	hourlyEvents, err := queryBucketCounts(ctx, db, "live_events", "created_at", cutoffValue, "", granularity)
	if err != nil {
		return nil, err
	}
	hourlyDanmaku, err := queryBucketCounts(ctx, db, "danmaku_records", "created_at", cutoffValue, "", granularity)
	if err != nil {
		return nil, err
	}
	alertTrend, err := queryBucketCounts(ctx, db, "live_events", "created_at", cutoffValue, "event_type = 'bilibili.api.alert.sent'", granularity)
	if err != nil {
		return nil, err
	}
	sessionStats, err := querySessionStats(ctx, db, cutoffValue)
	if err != nil {
		return nil, err
	}

	keywordStats := buildKeywordStats(ctx, s, cutoff)
	queueSummary, _ := s.store.IntegrationTaskSummary(ctx)
	deadTasks, _ := s.store.ListIntegrationTasks(ctx, 20, string(store.IntegrationTaskStatusDead), "")

	matchedTotal := ruleExecuted + ruleErrors
	hitRate := 0.0
	if matchedTotal > 0 {
		hitRate = float64(ruleExecuted) / float64(matchedTotal)
	}

	return map[string]any{
		"hours":       hours,
		"granularity": granularity,
		"cutoff":      cutoff.Format(time.RFC3339),
		"totals": map[string]any{
			"eventCount":       eventCount,
			"danmakuCount":     danmakuCount,
			"botCommands":      botCommands,
			"ruleExecuted":     ruleExecuted,
			"ruleErrors":       ruleErrors,
			"matchedTotal":     matchedTotal,
			"ruleHitRate":      hitRate,
			"alertSentCount":   alertCount,
			"taskQueuePending": queueSummary.Pending,
			"taskQueueDead":    queueSummary.Dead,
		},
		"eventTypeTop":  eventTypeTop,
		"hourlyEvents":  hourlyEvents,
		"hourlyDanmaku": hourlyDanmaku,
		"alertTrend":    alertTrend,
		"keywordStats":  keywordStats,
		"sessionStats":  sessionStats,
		"queueSummary":  queueSummary,
		"deadLetter":    deadTasks,
		"consumerState": s.ConsumerRuntime(),
		"now":           now.Format(time.RFC3339),
	}, nil
}

func queryEventTypeTop(ctx context.Context, db *sql.DB, cutoff string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 30
	}
	rows, err := db.QueryContext(ctx, `SELECT event_type, COUNT(1) AS total
	FROM live_events
	WHERE datetime(created_at) >= datetime(?)
	GROUP BY event_type
	ORDER BY total DESC, event_type ASC
	LIMIT ?`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, limit)
	for rows.Next() {
		var eventType string
		var total int64
		if err := rows.Scan(&eventType, &total); err != nil {
			return nil, err
		}
		items = append(items, map[string]any{
			"eventType": strings.TrimSpace(eventType),
			"count":     total,
		})
	}
	return items, rows.Err()
}

func normalizeStatsGranularity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "day", "daily":
		return "day"
	default:
		return "hour"
	}
}

func queryBucketCounts(ctx context.Context, db *sql.DB, table string, timeColumn string, cutoff string, whereExtra string, granularity string) ([]map[string]any, error) {
	layout := "%Y-%m-%dT%H:00:00Z"
	keyName := "hour"
	if normalizeStatsGranularity(granularity) == "day" {
		layout = "%Y-%m-%d"
		keyName = "day"
	}
	query := "SELECT strftime('" + layout + "', datetime(" + timeColumn + ")) AS bucket_key, COUNT(1) AS total FROM " + table +
		" WHERE datetime(" + timeColumn + ") >= datetime(?)"
	if strings.TrimSpace(whereExtra) != "" {
		query += " AND " + whereExtra
	}
	query += " GROUP BY bucket_key ORDER BY bucket_key ASC"

	rows, err := db.QueryContext(ctx, query, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]map[string]any, 0, 64)
	for rows.Next() {
		var hourKey sql.NullString
		var total int64
		if err := rows.Scan(&hourKey, &total); err != nil {
			return nil, err
		}
		key := ""
		if hourKey.Valid {
			key = strings.TrimSpace(hourKey.String)
		}
		if key == "" {
			continue
		}
		items = append(items, map[string]any{
			"bucket": key,
			keyName:  key,
			"count":  total,
		})
	}
	return items, rows.Err()
}

func querySessionStats(ctx context.Context, db *sql.DB, cutoff string) (map[string]any, error) {
	row := db.QueryRowContext(ctx, `SELECT
		COUNT(1) AS total_sessions,
		SUM(CASE WHEN ended_at IS NULL THEN 1 ELSE 0 END) AS running_sessions,
		AVG(CASE WHEN ended_at IS NOT NULL THEN (julianday(ended_at) - julianday(started_at)) * 86400 END) AS avg_seconds
	FROM stream_sessions
	WHERE datetime(started_at) >= datetime(?)`, cutoff)
	var totalSessions int64
	var runningSessions sql.NullInt64
	var avgSeconds sql.NullFloat64
	if err := row.Scan(&totalSessions, &runningSessions, &avgSeconds); err != nil {
		return nil, err
	}
	result := map[string]any{
		"totalSessions":   totalSessions,
		"runningSessions": int64(0),
		"avgSeconds":      0.0,
	}
	if runningSessions.Valid {
		result["runningSessions"] = runningSessions.Int64
	}
	if avgSeconds.Valid {
		result["avgSeconds"] = avgSeconds.Float64
	}
	return result, nil
}

func buildKeywordStats(ctx context.Context, svc *Service, cutoff time.Time) []map[string]any {
	executedEvents, _ := svc.store.ListLiveEventsByTypeSince(ctx, "danmaku.rule.executed", cutoff, 2000)
	failedEvents, _ := svc.store.ListLiveEventsByTypeSince(ctx, "danmaku.rule.error", cutoff, 2000)
	type counter struct {
		Executed int
		Failed   int
	}
	bucket := map[string]*counter{}
	appendEvent := func(items []store.LiveEvent, isExecuted bool) {
		for _, item := range items {
			payload := map[string]any{}
			if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil {
				continue
			}
			keyword := strings.TrimSpace(anyString(payload["keyword"]))
			if keyword == "" {
				continue
			}
			if _, ok := bucket[keyword]; !ok {
				bucket[keyword] = &counter{}
			}
			if isExecuted {
				bucket[keyword].Executed++
			} else {
				bucket[keyword].Failed++
			}
		}
	}
	appendEvent(executedEvents, true)
	appendEvent(failedEvents, false)

	result := make([]map[string]any, 0, len(bucket))
	for keyword, item := range bucket {
		total := item.Executed + item.Failed
		hitRate := 0.0
		if total > 0 {
			hitRate = float64(item.Executed) / float64(total)
		}
		result = append(result, map[string]any{
			"keyword":  keyword,
			"executed": item.Executed,
			"failed":   item.Failed,
			"total":    total,
			"hitRate":  hitRate,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		li := result[i]["total"].(int)
		lj := result[j]["total"].(int)
		if li == lj {
			return result[i]["keyword"].(string) < result[j]["keyword"].(string)
		}
		return li > lj
	})
	if len(result) > 30 {
		return result[:30]
	}
	return result
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strings.TrimSpace(strconv.FormatFloat(v, 'f', -1, 64))
	default:
		return ""
	}
}
