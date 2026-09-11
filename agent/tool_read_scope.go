package agent

import "github.com/Autumn-27/artex/db"

// Never attached to the shared ToolSet or reused across model requests. The
// provider still revalidates every returned asset/node before sending history.
type overviewReadScope struct {
	states      map[int64]map[int64]string
	sources     []db.DirectSourceStore
	sourcesErr  error
	sourcesRead bool
}

func (t *ToolSet) readApprovalStates(taskID int64, ids []int64) (map[int64]string, error) {
	if t.overviewReads == nil {
		return t.as.TaskAssetApprovalStates(taskID, ids)
	}
	known := t.overviewReads.states[taskID]
	if known == nil {
		known = map[int64]string{}
		t.overviewReads.states[taskID] = known
	}
	missing := []int64{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if _, ok := known[id]; !ok && !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	if len(missing) > 0 {
		states, err := t.as.TaskAssetApprovalStates(taskID, missing)
		if err != nil {
			return nil, err
		}
		for _, id := range missing {
			known[id] = states[id]
		}
	}
	return known, nil
}

func (t *ToolSet) directSourceStores() ([]db.DirectSourceStore, error) {
	if t.overviewReads == nil {
		return t.ts.DirectSourceStores()
	}
	if !t.overviewReads.sourcesRead {
		t.overviewReads.sources, t.overviewReads.sourcesErr = t.ts.DirectSourceStores()
		t.overviewReads.sourcesRead = true
	}
	return t.overviewReads.sources, t.overviewReads.sourcesErr
}
