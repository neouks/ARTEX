package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Autumn-27/artex/agent"
	"github.com/Autumn-27/artex/db"
)

func TestMainDispatchFeedbackPublishDoesNotRunMainOrPlanner(t *testing.T) {
	s, task := modeServer(t)
	taskID, _ := strconv.ParseInt(task.ID, 10, 64)
	task.Store.SetExecutionMode(db.ExecutionManual)
	id, err := task.Store.AddIntent(map[string]any{"summary": "worker feedback test"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	ctx := agent.WithRunInfo(agent.WithMainDispatchSession(t.Context(), 4), agent.RunInfo{TaskID: taskID, AgentKey: "mainagent"})
	results, err := s.dispatchTaskIntents(ctx, task, []int64{id, 999999999})
	if err != nil || len(results) != 2 || results[0].Status != "dispatched" || results[1].Status != "rejected" {
		t.Fatal(results, err)
	}
	if ok, err := task.Store.ClaimIntent(id, "test"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err = task.Store.AppendActivity(db.Activity{NodeID: &id, Worker: "test", Kind: "result", Detail: "final worker conclusion"}); err != nil {
		t.Fatal(err)
	}
	if err = task.Store.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.engine.bc.Subscribe(task.ID)
	defer unsub()
	s.chatBusy = map[string]bool{task.ID: true} // another user turn must remain untouched
	s.publishWorkerFeedback(t.Context())
	select {
	case a := <-ch:
		if !isWorkerFeedback(a) || a.MainSeg == nil || *a.MainSeg != 4 || !strings.Contains(a.Summary, "final worker conclusion") {
			t.Fatal(a)
		}
	case <-time.After(time.Second):
		t.Fatal("no feedback")
	}
	if !s.chatBusy[task.ID] || task.hasPendingTriggers() {
		t.Fatal("feedback mutated agent scheduling")
	}
	s.publishWorkerFeedback(t.Context())
	select {
	case a := <-ch:
		t.Fatal("duplicate publish", a)
	default:
	}
}

func TestWorkerFeedbackReplayAfterNewerActivityCursor(t *testing.T) {
	s, task := modeServer(t)
	id, err := task.Store.AddIntent(map[string]any{"summary": "SSE feedback"}, 5, nil, "human")
	if err != nil {
		t.Fatal(err)
	}
	if err = task.Store.MainDispatchFeedback(t.Context(), id, 2, true); err != nil {
		t.Fatal(err)
	}
	if err = task.Store.SetIntentState(id, "done"); err != nil {
		t.Fatal(err)
	}
	newer, err := task.Store.AppendActivity(db.Activity{Worker: "mainagent", Kind: "text", Summary: "newer user turn"})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.m.pg.UnpublishedWorkerFeedback(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	feedbackID := rows[0].Activity.ID
	ts := httptest.NewServer(http.HandlerFunc(s.streamActivity))
	defer ts.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"?task="+task.ID, nil)
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%d:0", newer))
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	lastID := ""
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "id: ") {
			lastID = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "data: ") {
			var dto struct {
				Seq int64 `json:"seq"`
			}
			if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &dto); err != nil {
				t.Fatal(err)
			}
			if dto.Seq == feedbackID {
				found = true
				break
			}
		}
	}
	if !found || lastID != fmt.Sprintf("%d:%d", newer, feedbackID) {
		t.Fatal("lost delayed feedback / cursor regressed", found, lastID, scanner.Err())
	}
	cancel()
	// Once both cursors have advanced, the feedback replay is empty.
	replay, err := task.Store.WorkerFeedbackReplay(t.Context(), feedbackID, newer)
	if err != nil || len(replay) != 0 {
		t.Fatal(replay, err)
	}
}
