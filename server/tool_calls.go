package server

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/traffic"
	actool "github.com/Autumn-27/norma/tool"
)

// Constructors are metadata-only; no Tool.Call, model or network access. Cache
// the immutable code catalogue, never conversation results or authorization.
var toolCallBuiltinNames = sync.OnceValue(func() []string {
	names := []string{actool.SearchExtraToolsName, actool.ExecuteExtraToolName, "web_search"}
	for _, seed := range agent.BuiltinToolSeeds() {
		names = append(names, seed.Key)
	}
	meta := append(actool.DefaultTools(), actool.ShellSessionTools()...)
	meta = append(meta, traffic.SeedToolMetas()...)
	meta = append(meta, actool.NewTodoStore().Tool(), actool.NewTaskList(), actool.NewTaskStop(), actool.NewTaskOutput(), actool.NewWebFetch(actool.WebFetchConfig{}))
	for _, t := range meta {
		names = append(names, t.Name())
	}
	return names
})

type toolCallCursor struct {
	Scope    string `json:"scope"`
	Q        string `json:"q"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Before   int64  `json:"before"`
	Snapshot int64  `json:"snapshot"`
}

func toolCallInt(r *http.Request, key string, def, min, max int) (int, error) {
	text := r.URL.Query().Get(key)
	if text == "" {
		return def, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s 必须为 %d–%d 的整数", key, min, max)
	}
	return n, nil
}

func parseToolCallQuery(r *http.Request, scope string) (db.ToolCallQuery, error) {
	q := db.ToolCallQuery{Q: strings.TrimSpace(r.URL.Query().Get("q")), Type: r.URL.Query().Get("type"), Status: r.URL.Query().Get("status")}
	for key, values := range r.URL.Query() {
		switch key {
		case "task", "session", "q", "type", "status", "limit", "cursor":
		default:
			return q, fmt.Errorf("未知参数 %s", key)
		}
		if len(values) != 1 {
			return q, fmt.Errorf("参数 %s 不可重复", key)
		}
	}
	if len(q.Q) > 500 {
		return q, fmt.Errorf("搜索文本过长")
	}
	switch q.Type {
	case "", "builtin", "custom", "mcp", "unknown":
	default:
		return q, fmt.Errorf("无效工具类型")
	}
	switch q.Status {
	case "", "success", "failed", "running", "missing":
	default:
		return q, fmt.Errorf("无效调用状态")
	}
	var err error
	q.Limit, err = toolCallInt(r, "limit", 20, 1, 50)
	if err != nil {
		return q, err
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		if len(raw) > 4096 {
			return q, fmt.Errorf("无效分页游标")
		}
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var c toolCallCursor
		if err != nil || json.Unmarshal(data, &c) != nil || c.Scope != scope || c.Q != q.Q || c.Type != q.Type || c.Status != q.Status || c.Before <= 0 || c.Snapshot < c.Before {
			return q, fmt.Errorf("无效或不匹配的分页游标")
		}
		q.Before, q.Snapshot = c.Before, c.Snapshot
	}
	return q, nil
}

func (s *Server) taskToolCalls(w http.ResponseWriter, r *http.Request) {
	t := s.m.ResolveTask(r.URL.Query().Get("task"))
	if t == nil || t.Store == nil {
		writeErr(w, 404, "task not found")
		return
	}
	f, ok := parseActivitySession(r.URL.Query().Get("session"))
	if !ok {
		writeErr(w, 400, "invalid session")
		return
	}
	if f.Main && f.MainSeg == nil {
		seg, err := t.Store.CurrentMainSeg()
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		f.MainSeg = &seg
	}
	store, source, err := activitySessionStore(t, f)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if store == nil {
		writeErr(w, 404, "session not found")
		return
	}
	pg := s.pg(w)
	if pg == nil {
		return
	}
	running := false
	if source == 0 && r.PathValue("seq") == "" {
		switch {
		case f.Main:
			current, err := t.Store.CurrentMainSeg()
			if err != nil {
				writeErr(w, 500, err.Error())
				return
			}
			s.chatMu.Lock()
			running = current == *f.MainSeg && s.chatBusy[t.ID]
			s.chatMu.Unlock()
		case f.NodeID != nil && s.engine != nil:
			s.engine.workMu.Lock()
			running = s.engine.work[*f.NodeID] != nil
			s.engine.workMu.Unlock()
		case f.Worker == "planner" && s.engine != nil:
			_, running = s.engine.plannerActive.Load(t.ID)
		}
	}
	key := fmt.Sprintf("task:%s:store:%d:session:%s", t.ID, store.ID(), r.URL.Query().Get("session"))
	if f.Main {
		key = fmt.Sprintf("task:%s:main:%d", t.ID, *f.MainSeg)
	}
	s.serveToolCalls(w, r, pg, store.ToolCallScope(f), key, running, source)
}

func (s *Server) conversationToolCalls(w http.ResponseWriter, r *http.Request) {
	pg, c, ok := s.convByID(w, r)
	if !ok {
		return
	}
	s.chatMu.Lock()
	running := s.chatBusy[s.convBusyKey(c.ID)]
	s.chatMu.Unlock()
	s.serveToolCalls(w, r, pg, db.ToolCallScope{ConversationID: c.ID}, fmt.Sprintf("conversation:%d", c.ID), running, 0)
}

func (s *Server) serveToolCalls(w http.ResponseWriter, r *http.Request, pg *db.DB, scope db.ToolCallScope, key string, running bool, source int64) {
	if raw := r.PathValue("seq"); raw != "" {
		for key, values := range r.URL.Query() {
			switch key {
			case "task", "session", "offset", "limit":
			default:
				writeErr(w, 400, "未知参数 "+key)
				return
			}
			if len(values) != 1 {
				writeErr(w, 400, "参数不可重复")
				return
			}
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, 400, "invalid record ID")
			return
		}
		offset, err := toolCallInt(r, "offset", 0, 0, 2147483646)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		limit, err := toolCallInt(r, "limit", 8000, 1, 24000)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		part, err := pg.ToolCallDetail(r.Context(), scope, id, offset, limit)
		if errors.Is(err, db.ErrToolCallRange) {
			writeErr(w, 400, err.Error())
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeErr(w, 404, "record not in session")
			return
		}
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, part)
		return
	}
	q, err := parseToolCallQuery(r, key)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	q.Running = running
	q.BuiltinNames = toolCallBuiltinNames()
	page, err := pg.ListToolCalls(r.Context(), scope, q)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	page.SourceTaskID, page.ReadOnly = source, source != 0
	if page.HasMore {
		snapshot := q.Snapshot
		if snapshot == 0 {
			snapshot = page.SnapshotCursor
		}
		data, _ := json.Marshal(toolCallCursor{Scope: key, Q: q.Q, Type: q.Type, Status: q.Status, Before: page.Items[len(page.Items)-1].ID, Snapshot: snapshot})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	writeJSON(w, http.StatusOK, page)
}
