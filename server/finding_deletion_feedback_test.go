package server

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFindingDeletionFeedbackHTTP(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := &Server{m: m}
	for _, tc := range []struct {
		body   string
		status int
	}{{"", 200}, {`{}`, 200}, {`{"reason":"  insufficient evidence  "}`, 200}, {`{"reason":" \n "}`, 200}, {`{"reason":1}`, 400}, {`{"reason":null}`, 400}, {`null`, 400}, {`{"reason":"x"} {}`, 400}, {`{`, 400}, {`{"reason":"` + strings.Repeat("a", 2001) + `"}`, 400}, {`{"unknown":true}`, 400}} {
		id, err := m.pg.AddFinding(0, 0, "XSS", "title", "high", "", "", "worker", nil)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("DELETE", "/", strings.NewReader(tc.body))
		r.SetPathValue("id", fmt.Sprint(id))
		w := httptest.NewRecorder()
		s.deleteFinding(w, r)
		if w.Code != tc.status {
			t.Fatalf("body=%q got %d %s", tc.body, w.Code, w.Body.String())
		}
		if tc.status == 200 {
			again := httptest.NewRecorder()
			r2 := httptest.NewRequest("DELETE", "/", nil)
			r2.SetPathValue("id", fmt.Sprint(id))
			s.deleteFinding(again, r2)
			if again.Code != 404 {
				t.Fatal("duplicate delete", again.Code)
			}
		} else {
			f, _ := m.pg.GetFinding(id)
			if f == nil {
				t.Fatal("invalid request deleted finding")
			}
		}
		m.pg.DeleteFinding(id)
	}
}

func TestFindingDeletionFeedbackPlannerOnlyWake(t *testing.T) {
	for _, status := range []string{"running", "paused", "done", "failed", "timeout"} {
		task := &Task{Status: status, notify: make(chan struct{}, 1)}
		worker := task.workerSignal()
		task.NotifyFindingDeleted(123)
		if task.hasPendingTriggers() != (status == "running") {
			t.Fatalf("status %s wrong notification", status)
		}
		select {
		case <-worker:
			t.Fatal("worker awakened")
		default:
		}
		if task.Status != status {
			t.Fatal("status changed")
		}
		if status == "running" {
			events := task.drainTriggers()
			if len(events) != 1 || events[0].FeedbackID != 123 || events[0].Detail != "" || len(events[0].Hints) != 0 {
				t.Fatalf("bad trigger %+v", events)
			}
		}
	}
	task := &Task{Status: "running", Paused: true, notify: make(chan struct{}, 1)}
	task.NotifyFindingDeleted(1)
	if task.hasPendingTriggers() {
		t.Fatal("paused task awakened")
	}
}
