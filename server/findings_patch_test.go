package server

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestPatchFindingValidatesBeforeWriting(t *testing.T) {
	m, err := NewManager(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	id, err := m.pg.AddFinding(0, 0, "XSS", "before", "high", "summary", "evidence", "worker", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer m.pg.DeleteFinding(id)
	s := &Server{m: m}
	r := httptest.NewRequest("PATCH", "/", strings.NewReader(`{"status":"resolved","severity":"invalid"}`))
	r.SetPathValue("id", strconv.FormatInt(id, 10))
	w := httptest.NewRecorder()
	s.patchFinding(w, r)
	if w.Code != 400 {
		t.Fatalf("want 400, got %d: %s", w.Code, w.Body.String())
	}
	f, err := m.pg.GetFinding(id)
	if err != nil || f.Status != "pending" {
		t.Fatalf("failed patch changed finding: %+v %v", f, err)
	}
}
