package server

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestNotificationSnapshotValidation(t *testing.T) {
	for _, value := range []string{"", "10:10:", "10:20:10,12,19"} {
		if !validNotificationSnapshot(value) {
			t.Errorf("rejected %q", value)
		}
	}
	for _, value := range []string{"bad", "0:1:", "20:10:", "10:20:20", "10:20:11,10", "99999999999999999999:20:", "10:20:9"} {
		if validNotificationSnapshot(value) {
			t.Errorf("accepted %q", value)
		}
	}
}

func TestTaskNotificationsHTTPValidationAndReadOnly(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	task, err := p.CreateTaskWithOptions("notifications http", "", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer p.DeleteTask(task.ID)
	s := &Server{m: &Manager{pg: p}}
	call := func(raw, mode string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/tasks/notifications?"+url.Values{"queries": {raw}, "mode": {mode}}.Encode(), nil)
		w := httptest.NewRecorder()
		s.taskNotifications(w, r)
		return w
	}
	for _, raw := range []string{`[]`, `null`, `[{"task_id":"-1"}]`, `[{"task_id":"1","bad":1}]`, `[{"task_id":"1"},{"task_id":"1"}]`, `[{"task_id":"1","findings":"1:0:"}]`, `[{"task_id":"1"}] {}`} {
		if w := call(raw, "all"); w.Code != 400 {
			t.Fatalf("%s: %d %s", raw, w.Code, w.Body)
		}
	}
	queries := []db.TaskNotificationQuery{{TaskID: strconv.FormatInt(task.ID, 10)}}
	raw, _ := json.Marshal(queries)
	if w := call(string(raw), "bad"); w.Code != 400 {
		t.Fatal(w.Code)
	}
	var before, after int
	if err = p.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w := call(string(raw), "all")
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var result db.TaskNotificationSummary
	if err = json.Unmarshal(w.Body.Bytes(), &result); err != nil || len(result.Items) != 1 || result.Snapshot == "" {
		t.Fatalf("%s %v", w.Body, err)
	}
	if err = p.QueryRow(`SELECT count(*) FROM findings WHERE task_id=$1`, task.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("read changed findings")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("summary may be stale cached")
	}
}
