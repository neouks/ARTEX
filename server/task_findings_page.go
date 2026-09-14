package server

import (
	"github.com/Autumn-27/artex/db"
	"net/http"
	"strconv"
)

func (s *Server) taskFindingsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	taskID, err := strconv.ParseInt(q.Get("context_task"), 10, 64)
	if err != nil || taskID <= 0 {
		writeErr(w, 400, "invalid context_task")
		return
	}
	if s.m.ResolveTask(q.Get("context_task")) == nil {
		writeErr(w, 404, "task not found")
		return
	}
	if q.Has("legacy_node") {
		nodeID, e := strconv.ParseInt(q.Get("legacy_node"), 10, 64)
		if e != nil || nodeID <= 0 {
			writeErr(w, 400, "invalid legacy_node")
			return
		}
		row, e := s.m.pg.GetTaskLegacyFinding(r.Context(), taskID, nodeID)
		if e != nil {
			writeErr(w, 500, e.Error())
			return
		}
		if row == nil {
			writeErr(w, 404, "legacy finding not found in task")
			return
		}
		item := findingFromDB(row, s.resolveFindingAssets([]*db.DBFinding{row}))
		item.ID = i64s(nodeID)
		item.FindingID = ""
		if row.TaskID != nil && *row.TaskID != taskID {
			item.Inherited = true
			item.SourceTaskID = i64s(*row.TaskID)
		}
		writeJSON(w, 200, item)
		return
	}
	page, limit := 1, 20
	for key, dest := range map[string]*int{"page": &page, "limit": &limit} {
		if raw := q.Get(key); raw != "" {
			value, e := strconv.Atoi(raw)
			if e != nil {
				writeErr(w, 400, "invalid pagination")
				return
			}
			*dest = value
		}
	}
	if page < 1 || page > 1000000 || limit < 1 || limit > 200 {
		writeErr(w, 400, "invalid pagination")
		return
	}
	direction := q.Get("direction")
	if direction != "" && direction != "asc" && direction != "desc" {
		writeErr(w, 400, "invalid direction")
		return
	}
	rows, total, err := s.m.pg.ListTaskFindingsPage(r.Context(), taskID, page, limit, direction == "asc")
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	assets := s.resolveFindingAssets(rows)
	items := make([]FindingDTO, 0, len(rows))
	for _, row := range rows {
		item := findingFromDB(row, assets)
		if row.ID == 0 {
			item.FindingID = ""
			if row.NodeID != nil {
				item.ID = i64s(*row.NodeID)
			}
		}
		if row.TaskID != nil && *row.TaskID != taskID {
			item.Inherited = true
			item.SourceTaskID = i64s(*row.TaskID)
		}
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "page": page, "page_size": limit})
}
