package agent

import (
	"github.com/Autumn-27/artex/db"
	"testing"
)

func TestChatTaskPolicyDoesNotMutateSharedAgent(t *testing.T) {
	base := NewChatAgent(nil, "model", t.TempDir(), nil, 0)
	as := &db.AssetStore{}
	bound := base.WithTaskPolicy(as, 42, "", "", nil)
	other := base.WithTaskPolicy(as, 99, "", "", nil)
	if base.taskID != 0 || base.taskAssets != nil || bound.taskID != 42 || other.taskID != 99 || bound == base {
		t.Fatal("task policy leaked into shared conversation agent")
	}
}
