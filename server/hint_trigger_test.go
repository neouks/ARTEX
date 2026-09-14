package server

import "testing"

func TestTaskNotifyHint(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	task.NotifyHint(nil)
	if task.hasPendingTriggers() {
		t.Fatal("empty hint triggered planner")
	}
	task.NotifyHint([]string{"one", "two"})
	events := task.drainTriggers()
	if len(events) != 1 || events[0].Kind != "hint" || len(events[0].Hints) != 2 {
		t.Fatalf("events=%+v", events)
	}
	select {
	case <-task.notify:
	default:
		t.Fatal("planner not notified")
	}
}
