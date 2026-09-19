package server

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

func (s *Server) explorationNodeDetail(w http.ResponseWriter, r *http.Request) {
	t, ok := s.m.Task(r.URL.Query().Get("task"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad node id")
		return
	}
	offset := func(key string) (int, error) {
		v := r.URL.Query().Get(key)
		if v == "" {
			return 0, nil
		}
		return strconv.Atoi(v)
	}
	body, err := offset("body_offset")
	if err != nil || body < -1 {
		writeErr(w, 400, "bad body offset")
		return
	}
	edge, err := offset("edge_offset")
	if err != nil || edge < -1 {
		writeErr(w, 400, "bad edge offset")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d, err := t.Store.BroadcastNode(ctx, id, body, edge)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if d == nil {
		writeErr(w, 404, "node not found")
		return
	}
	refs := map[string]TaskNodeDTO{}
	for _, n := range d.Refs {
		refs[i64s(n.ID)] = taskNodeDTO(n)
	}
	writeJSON(w, 200, map[string]any{"node": taskNodeDTO(d.Node), "payload": d.Payload, "payload_next_offset": d.PayloadNext, "edges": edgeDTOs(d.Edges), "edges_next_offset": d.EdgesNext, "refs": refs})
}
