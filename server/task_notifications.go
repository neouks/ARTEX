package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

var notificationSnapshotPattern = regexp.MustCompile(`^[0-9]{1,20}:[0-9]{1,20}:(?:[0-9]{1,20}(?:,[0-9]{1,20})*)?$`)

func validNotificationSnapshot(cursor string) bool {
	if cursor == "" {
		return true
	}
	if len(cursor) > 16384 || !notificationSnapshotPattern.MatchString(cursor) {
		return false
	}
	parts := strings.Split(cursor, ":")
	min, e1 := strconv.ParseUint(parts[0], 10, 64)
	max, e2 := strconv.ParseUint(parts[1], 10, 64)
	if e1 != nil || e2 != nil || min == 0 || min > max {
		return false
	}
	if parts[2] == "" {
		return true
	}
	var previous uint64
	for _, raw := range strings.Split(parts[2], ",") {
		x, e := strconv.ParseUint(raw, 10, 64)
		if e != nil || x < min || x >= max || x <= previous {
			return false
		}
		previous = x
	}
	return true
}

func (s *Server) taskNotifications(w http.ResponseWriter, r *http.Request) {
	for key, values := range r.URL.Query() {
		if (key != "queries" && key != "mode") || len(values) != 1 {
			writeErr(w, 400, "未知或重复提醒参数")
			return
		}
	}
	var queries []db.TaskNotificationQuery
	raw := r.URL.Query().Get("queries")
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 65536 || decoder.Decode(&queries) != nil || decoder.Decode(new(any)) != io.EOF || len(queries) == 0 || len(queries) > 100 {
		writeErr(w, 400, "提醒查询需要 1–100 个任务及有效游标")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode != "all" && mode != "findings" {
		writeErr(w, 400, "无效提醒类别")
		return
	}
	seen := make(map[string]bool)
	for _, q := range queries {
		id, err := strconv.ParseInt(q.TaskID, 10, 64)
		if err != nil || id <= 0 || strconv.FormatInt(id, 10) != q.TaskID || seen[q.TaskID] {
			writeErr(w, 400, "无效或重复任务 ID")
			return
		}
		seen[q.TaskID] = true
		for _, cursor := range []string{q.Findings, q.Assets, q.Intercepts} {
			if !validNotificationSnapshot(cursor) {
				writeErr(w, 400, "无效提醒游标")
				return
			}
		}
	}
	p := s.pg(w)
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := p.TaskNotifications(ctx, queries, mode == "findings")
	if err != nil {
		writeErr(w, 500, "读取提醒失败")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, result)
}
