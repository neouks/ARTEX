package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Autumn-27/artex/agent"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerQueueAndDeleteAPI(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	task, err := m.CreateTask("queue API", "test", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer m.DeleteTask(task.ID, DeleteTaskOptions{})
	s := newAdmissionTestServer(m, nil)
	a, err := task.Store.AddIntent(map[string]any{"summary": "a"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	b, err := task.Store.AddIntent(map[string]any{"summary": "b"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	call := func(method string, id int64, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := httptest.NewRequest(method, "/", bytes.NewReader(raw))
		r.SetPathValue("id", task.ID)
		r.SetPathValue("iid", fmt.Sprint(id))
		w := httptest.NewRecorder()
		if method == "DELETE" {
			s.deleteWorker(w, r)
		} else {
			s.workerQueue(w, r)
		}
		return w
	}
	q, _ := task.Store.WorkerQueue()
	w := call("POST", 0, map[string]any{"id": fmt.Sprint(a), "before_id": fmt.Sprint(b), "version": q.Version})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	w = call("POST", 0, map[string]any{"id": fmt.Sprint(b), "before_id": nil, "version": q.Version})
	if w.Code != 409 {
		t.Fatal("stale version accepted", w.Code)
	}
	transcriptDir := filepath.Join(m.dir, "transcripts")
	if err = os.MkdirAll(transcriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	removedPath := filepath.Join(transcriptDir, agent.WorkerSessionID(task.Store.ID(), a)+".jsonl")
	keptPath := filepath.Join(transcriptDir, agent.WorkerSessionID(task.Store.ID(), b)+".jsonl")
	for _, path := range []string{removedPath, keptPath} {
		if err = os.WriteFile(path, []byte("test transcript"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	w = call("DELETE", a, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err = os.Stat(removedPath); !os.IsNotExist(err) {
		t.Fatal("deleted transcript remains", err)
	}
	if _, err = os.Stat(keptPath); err != nil {
		t.Fatal("unrelated transcript removed", err)
	}
	notifications := task.drainTriggers()
	if len(notifications) != 1 {
		t.Fatalf("delete notification: %+v", notifications)
	}
	w = call("DELETE", a, nil)
	if w.Code != 200 || len(task.drainTriggers()) != 0 {
		t.Fatal("duplicate delete not idempotent")
	}
	n := s.engine.claimNext(task, "worker")
	if n == nil || n.ID != b {
		t.Fatal("wrong next worker", n)
	}
	w = call("DELETE", b, nil)
	if w.Code != 409 {
		t.Fatal("running delete accepted", w.Code)
	}
}
