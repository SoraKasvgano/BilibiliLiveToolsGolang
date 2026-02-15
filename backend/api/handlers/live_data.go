package handlers

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bilibililivetools/gover/backend/httpapi"
	"bilibililivetools/gover/backend/router"
	"bilibililivetools/gover/backend/store"
)

type liveDataModule struct {
	deps *router.Dependencies
}

func init() {
	router.Register(func(deps *router.Dependencies) router.Module {
		return &liveDataModule{deps: deps}
	})
}

func (m *liveDataModule) Prefix() string {
	return m.deps.Config.APIBase + "/live"
}

func (m *liveDataModule) Routes() []router.Route {
	return []router.Route{
		{Method: http.MethodGet, Pattern: "/events", Summary: "List live events", Handler: m.listEvents},
		{Method: http.MethodPost, Pattern: "/danmaku", Summary: "Insert danmaku record", Handler: m.insertDanmaku},
		{Method: http.MethodGet, Pattern: "/danmaku", Summary: "List danmaku records", Handler: m.listDanmaku},
		{Method: http.MethodGet, Pattern: "/stats", Summary: "Basic live statistics", Handler: m.stats},
		{Method: http.MethodGet, Pattern: "/stats/advanced", Summary: "Advanced live statistics", Handler: m.advancedStats},
		{Method: http.MethodGet, Pattern: "/stats/advanced/export", Summary: "Export advanced statistics to csv/json", Handler: m.exportAdvancedStats},
	}
}

func (m *liveDataModule) listEvents(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	items, err := m.deps.Integration.ListLiveEvents(r.Context(), limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *liveDataModule) insertDanmaku(w http.ResponseWriter, r *http.Request) {
	var req store.DanmakuRecord
	if err := httpapi.DecodeJSON(r, &req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusBadRequest)
		return
	}
	if req.RoomID <= 0 {
		httpapi.Error(w, -1, "roomId is required", http.StatusOK)
		return
	}
	if req.Content == "" {
		httpapi.Error(w, -1, "content is required", http.StatusOK)
		return
	}
	if err := m.deps.Integration.InsertDanmakuRecord(r.Context(), req); err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OKMessage(w, "Success")
}

func (m *liveDataModule) listDanmaku(w http.ResponseWriter, r *http.Request) {
	limit := parseIntOrDefault(r.URL.Query().Get("limit"), 100)
	roomID := int64(0)
	if raw := r.URL.Query().Get("roomId"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			roomID = parsed
		}
	}
	items, err := m.deps.Integration.ListDanmakuRecords(r.Context(), roomID, limit)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, items)
}

func (m *liveDataModule) stats(w http.ResponseWriter, r *http.Request) {
	hours := parseIntOrDefault(r.URL.Query().Get("hours"), 24)
	if hours <= 0 {
		hours = 24
	}
	cutoff := time.Now().Add(-time.Duration(hours) * time.Hour)
	eventCount, err := m.deps.Integration.CountLiveEventsSince(r.Context(), cutoff)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	danmakuCount, err := m.deps.Integration.CountDanmakuRecordsSince(r.Context(), 0, cutoff)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, map[string]any{
		"hours":        hours,
		"eventCount":   eventCount,
		"danmakuCount": danmakuCount,
		"now":          time.Now().Format(time.RFC3339),
	})
}

func (m *liveDataModule) advancedStats(w http.ResponseWriter, r *http.Request) {
	hours := parseIntOrDefault(r.URL.Query().Get("hours"), 24)
	granularity := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("granularity")))
	if granularity == "" {
		granularity = "hour"
	}
	result, err := m.deps.Integration.BuildAdvancedStats(r.Context(), hours, granularity)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	httpapi.OK(w, result)
}

func (m *liveDataModule) exportAdvancedStats(w http.ResponseWriter, r *http.Request) {
	hours := parseIntOrDefault(r.URL.Query().Get("hours"), 24)
	granularity := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("granularity")))
	if granularity == "" {
		granularity = "hour"
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	fields := parseAdvancedExportFields(r.URL.Query().Get("fields"))
	maxRows := parseIntOrDefault(r.URL.Query().Get("maxRows"), 300)
	if maxRows < 20 {
		maxRows = 20
	}
	if maxRows > 5000 {
		maxRows = 5000
	}
	if format == "" {
		format = "csv"
	}
	result, err := m.deps.Integration.BuildAdvancedStats(r.Context(), hours, granularity)
	if err != nil {
		httpapi.Error(w, -1, err.Error(), http.StatusOK)
		return
	}
	exportView := buildAdvancedStatsExportView(result, fields, maxRows)
	fileSuffix := time.Now().UTC().Format("20060102_150405")
	switch format {
	case "json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"advanced_stats_%s.json\"", fileSuffix))
		httpapi.OK(w, exportView)
		return
	case "csv":
		body, buildErr := buildAdvancedStatsCSV(exportView)
		if buildErr != nil {
			httpapi.Error(w, -1, buildErr.Error(), http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"advanced_stats_%s.csv\"", fileSuffix))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	default:
		httpapi.Error(w, -1, "unsupported format, use csv/json", http.StatusOK)
		return
	}
}

func buildAdvancedStatsCSV(result map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)
	writeTitle := func(title string) error {
		if err := writer.Write([]string{}); err != nil {
			return err
		}
		return writer.Write([]string{title})
	}
	totals, _ := result["totals"].(map[string]any)
	if err := writer.Write([]string{"section", "key", "value"}); err != nil {
		return nil, err
	}
	for key, value := range totals {
		if err := writer.Write([]string{"totals", key, fmt.Sprintf("%v", value)}); err != nil {
			return nil, err
		}
	}

	if err := writeCSVRows(writer, writeTitle, "hourlyEvents", []string{"bucket", "count"}, toRows(result["hourlyEvents"])); err != nil {
		return nil, err
	}
	if err := writeCSVRows(writer, writeTitle, "hourlyDanmaku", []string{"bucket", "count"}, toRows(result["hourlyDanmaku"])); err != nil {
		return nil, err
	}
	if err := writeCSVRows(writer, writeTitle, "alertTrend", []string{"bucket", "count"}, toRows(result["alertTrend"])); err != nil {
		return nil, err
	}
	if err := writeCSVRows(writer, writeTitle, "eventTypeTop", []string{"eventType", "count"}, toRows(result["eventTypeTop"])); err != nil {
		return nil, err
	}
	if err := writeCSVRows(writer, writeTitle, "keywordStats", []string{"keyword", "executed", "failed", "total", "hitRate"}, toRows(result["keywordStats"])); err != nil {
		return nil, err
	}
	if err := writeCSVRows(writer, writeTitle, "deadLetter", []string{"id", "taskType", "status", "attempt", "maxAttempts", "lastError", "updatedAt"}, toRows(result["deadLetter"])); err != nil {
		return nil, err
	}

	if sessionStats := toMap(result["sessionStats"]); len(sessionStats) > 0 {
		if err := writeTitle("sessionStats"); err != nil {
			return nil, err
		}
		if err := writer.Write([]string{"key", "value"}); err != nil {
			return nil, err
		}
		for key, value := range sessionStats {
			if err := writer.Write([]string{key, fmt.Sprintf("%v", value)}); err != nil {
				return nil, err
			}
		}
	}
	if queueSummary := toMap(result["queueSummary"]); len(queueSummary) > 0 {
		if err := writeTitle("queueSummary"); err != nil {
			return nil, err
		}
		if err := writer.Write([]string{"key", "value"}); err != nil {
			return nil, err
		}
		for key, value := range queueSummary {
			if err := writer.Write([]string{key, fmt.Sprintf("%v", value)}); err != nil {
				return nil, err
			}
		}
	}

	writer.Flush()
	return buf.Bytes(), writer.Error()
}

func writeCSVRows(writer *csv.Writer, writeTitle func(string) error, section string, columns []string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}
	if err := writeTitle(section); err != nil {
		return err
	}
	if err := writer.Write(columns); err != nil {
		return err
	}
	for _, row := range rows {
		record := make([]string, 0, len(columns))
		for _, col := range columns {
			record = append(record, fmt.Sprintf("%v", row[col]))
		}
		if err := writer.Write(record); err != nil {
			return err
		}
	}
	return nil
}

func parseAdvancedExportFields(raw string) map[string]bool {
	result := map[string]bool{}
	presets := map[string][]string{
		"all":    {"all"},
		"basic":  {"totals", "hourlyEvents", "hourlyDanmaku", "eventTypeTop"},
		"ops":    {"totals", "hourlyEvents", "hourlyDanmaku", "eventTypeTop", "keywordStats", "sessionStats", "queueSummary", "consumerState"},
		"alerts": {"totals", "alertTrend", "eventTypeTop", "deadLetter"},
	}
	for _, item := range strings.Split(raw, ",") {
		key := strings.ToLower(strings.TrimSpace(item))
		if key == "" {
			continue
		}
		if fields, ok := presets[key]; ok {
			for _, field := range fields {
				result[field] = true
			}
			continue
		}
		result[key] = true
	}
	return result
}

func buildAdvancedStatsExportView(result map[string]any, fields map[string]bool, maxRows int) map[string]any {
	if maxRows <= 0 {
		maxRows = 300
	}
	includeAll := len(fields) == 0 || fields["all"]
	include := func(name string) bool {
		if includeAll {
			return true
		}
		return fields[name]
	}
	view := map[string]any{
		"hours":       result["hours"],
		"granularity": result["granularity"],
		"cutoff":      result["cutoff"],
		"now":         result["now"],
	}
	if totals, ok := result["totals"].(map[string]any); ok {
		view["totals"] = totals
	}
	copyRows := func(key string) {
		if !include(key) {
			return
		}
		rows := toRows(result[key])
		if len(rows) > maxRows {
			rows = rows[:maxRows]
		}
		view[key] = rows
	}
	copyRows("hourlyEvents")
	copyRows("hourlyDanmaku")
	copyRows("eventTypeTop")
	copyRows("keywordStats")
	copyRows("alertTrend")
	copyRows("deadLetter")

	if include("sessionStats") {
		if item := toMap(result["sessionStats"]); len(item) > 0 {
			view["sessionStats"] = item
		}
	}
	if include("queueSummary") {
		if item := toMap(result["queueSummary"]); len(item) > 0 {
			view["queueSummary"] = item
		}
	}
	if include("consumerState") {
		view["consumerState"] = result["consumerState"]
	}
	return view
}

func toRows(value any) []map[string]any {
	list, ok := value.([]map[string]any)
	if ok {
		return list
	}
	raw, ok := value.([]any)
	if !ok {
		return []map[string]any{}
	}
	result := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		result = append(result, row)
	}
	return result
}

func toMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if item, ok := value.(map[string]any); ok {
		return item
	}
	body, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	result := map[string]any{}
	if err := json.Unmarshal(body, &result); err != nil {
		return map[string]any{}
	}
	return result
}
