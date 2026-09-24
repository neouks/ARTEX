package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/Autumn-27/artex/db"
)

// The database commits feedback with the Worker state change. This bounded
// outbox pump only publishes existing activities; it never runs an agent or
// touches chatBusy. Clients also recover these IDs through history/SSE catchup.
func (s *Server) publishWorkerFeedback(ctx context.Context) {
	if s.m.pg == nil {
		return
	}
	rows, err := s.m.pg.UnpublishedWorkerFeedback(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[worker feedback] read outbox: %v", err)
		}
		return
	}
	for _, r := range rows {
		s.engine.bc.Publish(r.TaskID, r.Activity)
		s.engine.touch(r.TaskID)
		if err := s.m.pg.MarkWorkerFeedbackPublished(ctx, r.Activity.ID); err != nil {
			log.Printf("[worker feedback] acknowledge %d: %v", r.Activity.ID, err)
			return
		}
	}
}
func (s *Server) workerFeedbackLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		s.publishWorkerFeedback(ctx)
		cancel()
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func isWorkerFeedback(a db.Activity) bool {
	var meta struct {
		Feedback json.RawMessage `json:"worker_feedback"`
	}
	return json.Unmarshal(a.Metadata, &meta) == nil && len(meta.Feedback) > 0 && string(meta.Feedback) != "null"
}
