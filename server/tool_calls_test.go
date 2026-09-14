package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func TestToolCallQueryValidation(t *testing.T) {
	for _, raw := range []string{"limit=0", "limit=51", "limit=x", "type=anything", "status=pending", "limit=2&limit=3", "url=other", "cursor=garbage"} {
		if _, err := parseToolCallQuery(httptest.NewRequest("GET", "/?"+raw, nil), "session"); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	data, _ := json.Marshal(toolCallCursor{Scope: "session", Q: "Bash", Before: 12, Snapshot: 20})
	cursor := base64.RawURLEncoding.EncodeToString(data)
	r := httptest.NewRequest("GET", "/?q=Bash&cursor="+cursor, nil)
	q, err := parseToolCallQuery(r, "session")
	if err != nil || q.Before != 12 || q.Limit != 20 {
		t.Fatalf("query %+v %v", q, err)
	}
	if _, err = parseToolCallQuery(r, "other"); err == nil {
		t.Fatal("cross session cursor accepted")
	}
	if _, err = parseToolCallQuery(httptest.NewRequest("GET", "/?q=Read&cursor="+cursor, nil), "session"); err == nil {
		t.Fatal("changed filter accepted")
	}
}

func TestToolCallsHTTPIsolationAndZeroModelWrites(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	if err = pg.EnsureLLMUsageTable(); err != nil {
		t.Fatal(err)
	}
	m := &Manager{pg: pg, tasks: map[string]*Task{}}
	s := &Server{m: m, chatBusy: map[string]bool{}, engine: &Engine{work: map[int64]*workExecution{}}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /conversations/{id}/tool-calls", s.conversationToolCalls)
	mux.HandleFunc("GET /conversations/{id}/tool-calls/{seq}", s.conversationToolCalls)
	mux.HandleFunc("GET /exploration/tool-calls", s.taskToolCalls)
	mux.HandleFunc("GET /exploration/tool-calls/{seq}", s.taskToolCalls)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		return w
	}
	c, err := pg.CreateConversation("mainagent", "tool HTTP", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteConversation(c.ID)
	other, err := pg.CreateConversation("mainagent", "other", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteConversation(other.ID)
	base := fmt.Sprintf("/conversations/%d/tool-calls", c.ID)
	var first int64
	for i := 0; i < 23; i++ {
		id, err := pg.AppendConvActivity(c.ID, db.Activity{Worker: "mainagent", Kind: "tool_use", Tool: "Bash", ToolUseID: strconv.Itoa(i), Detail: `{"command":"nothing executed"}`})
		if err != nil {
			t.Fatal(err)
		}
		if first == 0 {
			first = id
		}
	}
	counts := func() string {
		t.Helper()
		var llm, tools, events int
		for _, v := range []struct {
			table string
			n     *int
		}{{"llm_usage", &llm}, {"tool_usage", &tools}, {"conversation_activities", &events}} {
			if err := pg.QueryRow("SELECT count(*) FROM " + v.table).Scan(v.n); err != nil {
				t.Fatal(err)
			}
		}
		return fmt.Sprint(llm, tools, events)
	}
	before := counts()
	w := get(base)
	var p db.ToolCallPage
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Items) != 20 || !p.HasMore || p.Items[0].Type != "builtin" || p.Items[0].Status != "missing" {
		t.Fatalf("page %d %s", w.Code, w.Body.String())
	}
	w = get(base + "?cursor=" + url.QueryEscape(p.NextCursor))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Items) != 3 || p.HasMore {
		t.Fatalf("second page %+v", p)
	}
	for path, want := range map[string]int{base + "?q=Bash&type=builtin": 200, base + "?status=success": 200, fmt.Sprintf("%s/%d?limit=10", base, first): 200, fmt.Sprintf("%s/%d?offset=99999", base, first): 400, fmt.Sprintf("/conversations/%d/tool-calls/%d", other.ID, first): 404, base + "?limit=60": 400} {
		if w = get(path); w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if after := counts(); after != before {
		t.Fatalf("read view changed model/tool/activity ledgers: %s -> %s", before, after)
	}
	s.chatBusy[s.convBusyKey(c.ID)] = true
	w = get(base + "?status=running")
	if json.Unmarshal(w.Body.Bytes(), &p) != nil || len(p.Items) != 20 {
		t.Fatal("live state absent", w.Body.String())
	}
	// Source workers are readable only after terminal state; their main/planner
	// activity must never be reachable through an inherited worker detail ID.
	source, err := pg.CreateTask("source", "tools", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(source.ID)
	current, err := pg.CreateTaskWithOptions("current", "tools", db.TaskCreateOptions{SourceTaskIDs: []int64{source.ID}})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(current.ID)
	store := pg.Exploration(source.ExplorationID)
	intent, err := store.AddIntent(map[string]any{"summary": "source worker"}, 5, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	use, err := store.AppendActivity(db.Activity{Worker: "work#1", NodeID: &intent, Kind: "tool_use", Tool: "Bash", ToolUseID: "source", Detail: "source input"})
	if err != nil {
		t.Fatal(err)
	}
	mainID, err := store.AppendActivity(db.Activity{Worker: "mainagent", Kind: "tool_use", Tool: "Bash", ToolUseID: "source", Detail: "private source main"})
	if err != nil {
		t.Fatal(err)
	}
	tid := strconv.FormatInt(current.ID, 10)
	m.tasks[tid] = &Task{ID: tid, ExpID: current.ExplorationID, Store: pg.Exploration(current.ExplorationID)}
	params := fmt.Sprintf("?task=%s&session=intent:%d", tid, intent)
	if w = get("/exploration/tool-calls" + params); w.Code != 404 {
		t.Fatal("nonterminal inherited worker exposed", w.Body.String())
	}
	if err = store.SetIntentState(intent, "done"); err != nil {
		t.Fatal(err)
	}
	w = get("/exploration/tool-calls" + params)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || !p.ReadOnly || p.SourceTaskID != source.ID || len(p.Items) != 1 || p.Items[0].Status != "missing" {
		t.Fatalf("inherited %d %s", w.Code, w.Body.String())
	}
	if w = get(fmt.Sprintf("/exploration/tool-calls/%d%s", mainID, params)); w.Code != 404 {
		t.Fatal("source main leaked")
	}
	if w = get(fmt.Sprintf("/exploration/tool-calls/%d%s", use, params)); w.Code != 200 || !strings.Contains(w.Body.String(), "source input") {
		t.Fatal("source detail unavailable", w.Body.String())
	}
	if w = get(fmt.Sprintf("/exploration/tool-calls/%d?task=%s&session=main:0", use, tid)); w.Code != 404 {
		t.Fatal("cross-session worker detail leaked")
	}
}
