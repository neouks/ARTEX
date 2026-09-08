package server

import (
	"testing"

	"github.com/Autumn-27/artex/agent"
)

func TestTaskPendingTriggers(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	if task.hasPendingTriggers() {
		t.Fatal("new task unexpectedly has a pending trigger")
	}
	task.pendingTriggers = append(task.pendingTriggers, agent.TriggerEvent{Kind: "done", IntentID: 1})
	if !task.hasPendingTriggers() {
		t.Fatal("queued trigger was not observed")
	}
	if got := task.drainTriggers(); len(got) != 1 || task.hasPendingTriggers() {
		t.Fatalf("drainTriggers()=%v pending=%v", got, task.hasPendingTriggers())
	}
}
