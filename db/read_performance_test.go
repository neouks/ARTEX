package db

import (
	"context"
	"errors"
	"testing"
)

func TestAssetReadContextCancellation(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	shared := d.Assets()
	if _, err := shared.WithReadContext(ctx).CountsByType(); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled read, got %v", err)
	}
	if _, err := shared.CountsByType(); err != nil {
		t.Fatal(err)
	}
}

func TestGeneratedAuthorizationHostTracksWrites(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var id int64
	if err := d.QueryRow(`INSERT INTO assets(type,url,method) VALUES ('endpoint','https://AUTH-HOST.test:443/path','GET') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM assets WHERE id=$1`, id)
	for _, tc := range []struct{ raw, want string }{
		{"https://AUTH-HOST.test:443/path", "auth-host.test"},
		{"http://[2001:0db8::1]:80/path", "2001:db8::1"},
		{"https://internal/path", "internal"},
	} {
		var host string
		if err := d.QueryRow(`UPDATE assets SET url=$2 WHERE id=$1 RETURNING authorization_host`, id, tc.raw).Scan(&host); err != nil || host != tc.want {
			t.Fatalf("host for %q = %q, want %q: %v", tc.raw, host, tc.want, err)
		}
	}
	for _, value := range []string{"192.0.2.1", "192.0.2.0/24", "2001:db8::1", "::ffff:192.0.2.1"} {
		var ok bool
		if err := d.QueryRow(`SELECT try_inet($1)=$1::inet`, value).Scan(&ok); err != nil || !ok {
			t.Fatalf("inet %q: %v %v", value, ok, err)
		}
	}
}

func TestExecutionCountsAndOpenIntent(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.CreateExploration("aggregate-test", "test")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, id)
	es := d.Exploration(id)
	for _, node := range []struct{ kind, state string }{
		{KindGoal, "met"}, {KindGoal, "open"}, {KindIntent, "running"}, {KindIntent, "open"}, {KindIntent, "done"},
	} {
		if _, err := es.AddNode(node.kind, map[string]any{"text": "test"}, 1, node.state, "test", nil); err != nil {
			t.Fatal(err)
		}
	}
	running, goals, met, err := es.ExecutionCounts()
	if err != nil || running != 1 || goals != 2 || met != 1 {
		t.Fatalf("counts=%d/%d/%d err=%v", running, goals, met, err)
	}
	if open, err := es.HasOpenIntent(); err != nil || !open {
		t.Fatalf("open=%v err=%v", open, err)
	}
	if _, err := d.Exec(`UPDATE exploration_nodes SET state='done' WHERE exploration_id=$1 AND kind='intent' AND state='open'`, id); err != nil {
		t.Fatal(err)
	}
	if open, err := es.HasOpenIntent(); err != nil || open {
		t.Fatalf("only running remains: open=%v err=%v", open, err)
	}
}
