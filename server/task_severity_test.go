package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
)

func TestListTasksSeverityHTTP(t *testing.T) {
	s, task := modeServer(t)
	m := s.m
	taskID, err := strconv.ParseInt(task.ID, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	finding, err := m.pg.AddFinding(taskID, 0, "XSS", "fixture", "high", "", "", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteFinding(finding)
	check := func(want int) {
		t.Helper()
		w := httptest.NewRecorder()
		s.listTasks(w, httptest.NewRequest("GET", "/api/tasks", nil))
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			Tasks []TaskDTO `json:"tasks"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, dto := range out.Tasks {
			if dto.ID == task.ID {
				if dto.Findings.High != want || dto.Findings.Critical != 0 {
					t.Fatalf("findings=%+v", dto.Findings)
				}
				return
			}
		}
		t.Fatal("task missing")
	}
	check(1)
	if _, err := m.pg.DeleteFinding(finding); err != nil {
		t.Fatal(err)
	}
	check(0)
}
