package server

import (
	"sync"
	"testing"
)

func TestWorkerSignalBroadcastDoesNotConsumePlannerNotification(t *testing.T) {
	task := &Task{notify: make(chan struct{}, 1)}
	a, b := task.workerSignal(), task.workerSignal()
	task.Notify()
	for _, signal := range []<-chan struct{}{a, b, task.notify} {
		select {
		case <-signal:
		default:
			t.Fatal("notification missing")
		}
	}
	select {
	case <-task.workerSignal():
		t.Fatal("new generation is already closed")
	default:
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				_ = task.workerSignal()
				task.Notify()
			}
		})
	}
	wg.Wait()
	last := task.workerSignal()
	task.Notify()
	select {
	case <-last:
	default:
		t.Fatal("wake after concurrent notifications was lost")
	}
}
