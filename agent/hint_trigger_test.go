package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHintTriggerBatchAndFallback(t *testing.T) {
	d := testDB(t)
	defer d.Close()
	task, err := d.CreateTask("hint trigger test", "goal", nil, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d.DeleteTask(task.ID)
	ts := NewToolSet(d.Exploration(task.ExplorationID), "human")
	var calls, fallback int
	var texts []string
	ts.SetNotify(func() { fallback++ })
	ts.SetNotifyHint(func(v []string) { calls++; texts = v })
	_, err = ts.addHint().Call(t.Context(), json.RawMessage(`{"hints":[{"text":" first "},{"text":"second"}]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || fallback != 0 || !reflect.DeepEqual(texts, []string{"first", "second"}) {
		t.Fatalf("calls=%d fallback=%d texts=%v", calls, fallback, texts)
	}
	rendered := renderTriggers(ts.ts, []TriggerEvent{{Kind: "hint", Hints: texts}})
	if !strings.Contains(rendered, "2 条战略提示") || !strings.Contains(rendered, "first；second") {
		t.Fatal(rendered)
	}
	ts.SetNotifyHint(nil)
	_, err = ts.addHint().Call(t.Context(), json.RawMessage(`{"text":"fallback"}`), nil)
	if err != nil || fallback != 1 {
		t.Fatalf("fallback=%d err=%v", fallback, err)
	}
}
