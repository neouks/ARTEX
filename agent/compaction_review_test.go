package agent

import (
	"context"
	"testing"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/norma/llm"
)

func TestFoldBlockDiscardsChangedSource(t *testing.T) {
	for _, change := range []string{"unchanged", "revived", "edited"} {
		t.Run(change, func(t *testing.T) {
			d := testDB(t)
			defer d.Close()
			task, err := d.CreateTask("compaction source", "goal", nil, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer d.DeleteTask(task.ID)
			store := d.Exploration(task.ExplorationID)
			b := block{}
			for range 3 {
				id, err := store.AddNode(db.KindIntent, map[string]any{"summary": "original"}, 0, "done", "worker", nil)
				if err != nil {
					t.Fatal(err)
				}
				b.Members = append(b.Members, id)
			}
			g, nodes, err := loadColdGraph(store)
			if err != nil {
				t.Fatal(err)
			}
			versions, err := store.ContentVersions()
			if err != nil {
				t.Fatal(err)
			}
			provider := captureUsageProvider{stream: func(_ context.Context, yield func(llm.StreamEvent, error) bool) {
				switch change {
				case "revived":
					_, err = d.Exec(`UPDATE exploration_nodes SET state='open' WHERE id=$1`, b.Members[0])
				case "edited":
					_, err = d.Exec(`UPDATE exploration_nodes SET payload='{"summary":"changed"}'::jsonb,content_version=content_version+1 WHERE id=$1`, b.Members[0])
				}
				if err != nil {
					t.Fatal(err)
				}
				yield(llm.StreamEvent{Type: llm.SETextDelta, Text: "original conclusion"}, nil)
			}}
			NewCompactor(provider, "").foldBlock(t.Context(), store, g, b, nodes, versions, 1)
			digests, err := store.ActiveDigests()
			want := 0
			if change == "unchanged" {
				want = 1
			}
			if err != nil || len(digests) != want {
				t.Fatalf("digests=%d want=%d err=%v", len(digests), want, err)
			}
		})
	}
}
