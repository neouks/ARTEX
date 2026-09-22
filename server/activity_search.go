package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type activitySearchCursor struct {
	After int64  `json:"after"`
	Upper int64  `json:"upper"`
	Scope string `json:"scope"`
}

func (s *Server) activitySearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t := s.m.ResolveTask(q.Get("task"))
	if t == nil {
		writeErr(w, 404, "task not found")
		return
	}
	session := q.Get("session")
	f, ok := parseActivitySession(session)
	if !ok {
		writeErr(w, 400, "bad session")
		return
	}
	query := q.Get("q")
	if strings.TrimSpace(query) == "" || !utf8.ValidString(query) || utf8.RuneCountInString(query) > 200 {
		writeErr(w, 400, "query must contain 1–200 characters")
		return
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 50 {
			writeErr(w, 400, "bad limit")
			return
		}
	}
	if f.Main && f.MainSeg == nil {
		seg, err := t.Store.CurrentMainSeg()
		if err != nil {
			writeErr(w, 500, "session lookup failed")
			return
		}
		f.MainSeg = &seg
		session = fmt.Sprintf("main:%d", seg)
	}
	store, source, err := activitySessionStore(t, f)
	if err != nil {
		writeErr(w, 500, "session lookup failed")
		return
	}
	if store == nil {
		writeErr(w, 404, "session not found")
		return
	}
	f.Inherited = source > 0
	scope := fmt.Sprintf("%x", sha256.Sum256([]byte(t.ID+"\x00"+session+"\x00"+query)))
	c := activitySearchCursor{Scope: scope}
	if raw := q.Get("cursor"); raw != "" {
		b, e := base64.RawURLEncoding.DecodeString(raw)
		if len(raw) > 1024 || e != nil || json.Unmarshal(b, &c) != nil || c.Scope != scope || c.After <= 0 || c.Upper < c.After {
			writeErr(w, 400, "bad search cursor")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	hits, more, upper, err := store.ActivitySearch(ctx, f, query, c.After, c.Upper, limit)
	if err != nil {
		writeErr(w, 500, "history search failed; please retry")
		return
	}
	next := ""
	if more && len(hits) > 0 {
		b, _ := json.Marshal(activitySearchCursor{After: hits[len(hits)-1].ID, Upper: upper, Scope: scope})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	writeJSON(w, 200, map[string]any{"items": hits, "next_cursor": next, "source_task_id": source})
}
