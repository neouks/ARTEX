package server

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Autumn-27/artex/db"
)

func TestActivityBroadcastOverflowDisconnects(t *testing.T) {
	b := NewBroadcaster()
	ch, unsubscribe := b.Subscribe("task")
	defer unsubscribe()
	for id := 1; id <= cap(ch)+1; id++ {
		b.Publish("task", db.Activity{ID: int64(id)})
	}
	for id := 1; id <= cap(ch); id++ {
		if a, ok := <-ch; !ok || a.ID != int64(id) {
			t.Fatalf("buffer order %d: %+v %v", id, a, ok)
		}
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("overflow should close stream before any later cursor")
		}
	default:
		t.Fatal("overflow silently dropped data without disconnecting")
	}
	unsubscribe() // closing an already-evicted subscriber must be safe
	fresh, stop := b.Subscribe("task")
	defer stop()
	b.Publish("task", db.Activity{ID: 999})
	if a := <-fresh; a.ID != 999 {
		t.Fatal(a.ID)
	}
}

func TestActivityStreamConnectsBeforeReplayAndCancelsPoolWait(t *testing.T) {
	dsn, _, err := db.DSN()
	if err != nil {
		t.Skip(err)
	}
	pg, err := db.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	persisted, err := pg.CreateTaskWithOptions("SSE timing", "test", db.TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer pg.DeleteTask(persisted.ID)
	task := &Task{ID: strconv.FormatInt(persisted.ID, 10), Store: pg.Exploration(persisted.ExplorationID)}
	m := &Manager{tasks: map[string]*Task{task.ID: task}}
	s := &Server{m: m, engine: &Engine{bc: NewBroadcaster()}}
	pg.SetMaxOpenConns(1)
	conn, err := pg.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		s.streamActivity(w, r)
	}))
	defer ts.Close()
	defer conn.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"?task="+task.ID, nil)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal("headers waited for database replay:", err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("initial SSE frame %q: %v", line, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected SSE kept waiting for the database")
	}
}
