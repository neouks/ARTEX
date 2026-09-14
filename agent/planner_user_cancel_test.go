package agent

import (
	"strings"
	"testing"
)

func TestPlannerCancellationSurvivesTriggerDrain(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	exp, err := d.CreateExploration("test", "planner cancellation")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Exec(`DELETE FROM explorations WHERE id=$1`, exp)
	s := d.Exploration(exp)
	id, err := s.AddIntent(map[string]any{"summary": "avoid this direction"}, 1, nil, "planner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StopIntentWithReason(id, "user reason", "user"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		text, err := renderUserCancelledIntents(s)
		for _, want := range []string{"avoid this direction", "user reason", "仅用户手动重新开启", "同方向替代工作"} {
			if err != nil || !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s %v", want, text, err)
			}
		}
	}
	trigger := renderTriggers(s, []TriggerEvent{{Kind: "cancelled", IntentID: id, Detail: "user reason"}})
	if !strings.Contains(trigger, "不得主动重开") {
		t.Fatal(trigger)
	}
	if ok, err := s.ReopenIntentByUser(id, "stopped"); err != nil || !ok {
		t.Fatal(err)
	}
	stale := renderTriggers(s, []TriggerEvent{{Kind: "cancelled", IntentID: id, Detail: "user reason"}})
	if !strings.Contains(stale, "已由用户重新开启") {
		t.Fatal("stale trigger still blocks reopened work", stale)
	}

	if text, err := renderUserCancelledIntents(s); err != nil || text != "" {
		t.Fatalf("stale lock: %s %v", text, err)
	}
}
