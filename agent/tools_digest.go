package agent

// Cold digests are summarized in graph_overview; expand_digest reads details.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Autumn-27/artex/db"
	actool "github.com/Autumn-27/norma/tool"
)

// A digest body cannot be partially redacted reliably. Hide the whole body if
// any member is unavailable, and let authorized members remain visible unfolded.
func (t *ToolSet) digestAuthorizationBatch(store *db.ExplorationStore, ownerTaskID int64, ids []int64) (map[int64]bool, map[int64][]int64, error) {
	groups, err := store.ToolDigestMemberships(ids)
	if err != nil {
		return nil, nil, err
	}
	allowed := map[int64]bool{}
	members := map[int64][]int64{}
	var all []int64
	for _, g := range groups {
		members[g.ID] = g.Members
		all = append(all, g.Members...)
		all = append(all, g.Anchors...)
	}
	nodes, err := store.ToolNodesByIDs(all)
	if err != nil {
		return nil, nil, err
	}
	valid := map[int64]bool{}
	eligible := make([]*db.Node, 0, len(nodes))
	for _, n := range nodes {
		if n.Kind != db.KindDigest {
			eligible = append(eligible, n)
		}
	}
	checked, err := t.authorizedNodesForStore(eligible, store, ownerTaskID)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range checked {
		valid[n.ID] = true
	}
	for _, g := range groups {
		ok := len(g.Members) > 0
		for _, id := range append(append([]int64(nil), g.Members...), g.Anchors...) {
			ok = ok && valid[id]
		}
		allowed[g.ID] = ok
	}
	return allowed, members, nil
}

func (t *ToolSet) authorizedCoveredMembers(store *db.ExplorationStore, ownerTaskID int64) map[int64]int64 {
	covered, err := store.CoveredMembers()
	if err != nil {
		return nil
	}
	allowed := map[int64]bool{}
	var ids []int64
	for _, id := range covered {
		if _, seen := allowed[id]; !seen {
			allowed[id] = false
			ids = append(ids, id)
		}
	}
	allowed, _, err = t.digestAuthorizationBatch(store, ownerTaskID, ids)
	if err != nil {
		return nil
	}
	for member, id := range covered {
		if !allowed[id] {
			delete(covered, member)
		}
	}
	return covered
}

// coldDigestsRecent keeps the newest authorized digests and returns older IDs.
// Memberships and authorization are batched; no per-digest database reads.
func (t *ToolSet) coldDigestsRecent(store *db.ExplorationStore, ownerTaskID int64, limit int) ([]map[string]any, []int64, error) {
	ads, err := store.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(ads))
	for _, d := range ads {
		ids = append(ids, d.ID)
	}
	allowed, members, err := t.digestAuthorizationBatch(store, ownerTaskID, ids)
	if err != nil {
		return nil, nil, err
	}
	type entry struct {
		node      *db.Node
		freshness int64
	}
	entries := make([]entry, 0, len(ads))
	for _, d := range ads {
		if !allowed[d.ID] {
			continue
		}
		var freshness int64
		for _, id := range members[d.ID] {
			if id > freshness {
				freshness = id
			}
		}
		entries = append(entries, entry{d, freshness})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].freshness == entries[j].freshness {
			return entries[i].node.ID > entries[j].node.ID
		}
		return entries[i].freshness > entries[j].freshness
	})
	shown := make([]map[string]any, 0, min(limit, len(entries)))
	var more []int64
	for i, e := range entries {
		if i >= limit {
			more = append(more, e.node.ID)
			continue
		}
		var p struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(e.node.Payload, &p); err != nil {
			return nil, nil, err
		}
		shown = append(shown, map[string]any{"id": e.node.ID, "body": firstLine(p.Body, 1200), "body_is_summary": true, "member_count": len(members[e.node.ID])})
	}
	return shown, more, nil
}

// hiddenMembersFor returns a predicate telling whether a member is hidden (folded
// into an active digest AND still cold) in the given store — so a source task's
// overview folds exactly the way that task folds itself (§2 cross-task: "当前任务
// 什么展示逻辑，关联任务就什么逻辑"). A revived (now hot) covered member is NOT
// hidden (§6 render-time revival check). Returns a never-hidden predicate when the
// store has no digests.
func (t *ToolSet) hiddenMembersFor(store *db.ExplorationStore, ownerTaskID int64) func(int64) bool {
	covered := t.authorizedCoveredMembers(store, ownerTaskID)
	if len(covered) == 0 {
		return func(int64) bool { return false }
	}
	var hot map[int64]bool
	if cg, _, err := loadColdGraph(store); err == nil {
		hot = cg.hotSet()
	}
	return func(id int64) bool { _, c := covered[id]; return c && !hot[id] }
}

// resolveDigest finds a digest node by id in the current task, else in a direct
// source task (read-only, §2). Returns the node, its owning store, and the source
// task id (0 = current task).
func (t *ToolSet) resolveDigest(id int64) (*db.Node, *db.ExplorationStore, int64, error) {
	n, err := t.ts.GetNode(id)
	if err != nil {
		return nil, nil, 0, err
	}
	if n != nil && n.Kind == db.KindDigest {
		return n, t.ts, 0, nil
	}
	srcs, err := t.directSourceStores()
	if err != nil {
		return nil, nil, 0, err
	}
	for _, s := range srcs {
		n, err := s.Store.GetNode(id)
		if err != nil {
			return nil, nil, 0, err
		}
		if n != nil && n.Kind == db.KindDigest {
			return n, s.Store, s.Task.TaskID, nil
		}
	}
	return nil, nil, 0, nil
}

// expandDigest returns a digest's covered members as a compact list (§6.1). It is
// a distinct tool from node_detail because it returns a LIST of members, not one
// node's full detail.
func (t *ToolSet) expandDigest() actool.CoreTool {
	return t.writeExpTool("expand_digest",
		"展开一个 cold digest：返回它折叠的成员紧凑列表（id/summary/state/confidence），与概览 recent_facts/recent_done_intents 同形状。要某条完整细节/证据用 node_detail(member_id)。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "integer", "description": "digest 节点 id（来自概览 cold_digests / cold_digests_more）"},
				"limit": intp("成员默认20，最大100"), "before": intp("成员 next_before 续页"), "offset": intp("正文字符偏移"), "max_chars": intp("正文默认8000，最大24000"),
			},
			"required": []any{"id"},
		},
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			var in struct {
				ID     int64 `json:"id"`
				Limit  int   `json:"limit"`
				Before int64 `json:"before"`
				detailWindow
			}
			if err := decodeToolInput(raw, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := in.detailWindow.validate(); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.ID <= 0 || in.Before < 0 || in.Limit < 1 || in.Limit > 100 {
				return actool.Errorf("无效 digest 或分页参数"), nil
			}
			n, store, srcTaskID, resolveErr := t.resolveDigest(in.ID)
			if resolveErr != nil {
				return actool.Errorf(resolveErr.Error()), nil
			}
			if n == nil {
				return jsonResult(map[string]any{"error": fmt.Sprintf("#%d 不是 digest 节点（本任务或直接关联任务里都没找到）", in.ID)})
			}
			ownerTaskID := srcTaskID
			if ownerTaskID == 0 {
				ownerTaskID = t.taskID
			}
			allowed, _, authErr := t.digestAuthorizationBatch(store, ownerTaskID, []int64{n.ID})
			if authErr != nil {
				return actool.Errorf(authErr.Error()), nil
			}
			if !allowed[n.ID] {
				return actool.Errorf("digest 包含未授权资产，无法展开"), nil
			}
			var p struct {
				Body string `json:"body"`
			}
			_ = json.Unmarshal(n.Payload, &p)
			members, err := store.ToolDigestMemberPage(in.ID, in.Before, in.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			more := len(members) > in.Limit
			if more {
				members = members[:in.Limit]
			}
			list := make([]map[string]any, 0, len(members))
			for _, m := range members {
				entry := compactFact(m)
				if srcTaskID > 0 { // 关联任务的成员：只读，带继承标记（§2）
					entry["inherited"] = true
					entry["source_task_id"] = srcTaskID
				}
				list = append(list, entry)
			}
			out := map[string]any{
				"id":       in.ID,
				"state":    n.State, // active / superseded
				"members":  list,
				"has_more": more,
			}
			if more && len(list) > 0 {
				out["next_before"] = list[len(list)-1]["id"]
			}
			body, total, next := textWindow(p.Body, in.detailWindow)
			out["body"] = body
			out["body_total_chars"] = total
			out["body_truncated"] = next < total
			if next < total {
				out["next_offset"] = next
			}
			if srcTaskID > 0 {
				out["inherited"] = true
				out["source_task_id"] = srcTaskID
			}
			window := in.detailWindow
			for {
				encoded, err := json.Marshal(out)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if len([]rune(string(encoded))) <= toolListBudget {
					break
				}
				out["truncated"] = true
				if len(list) > 1 {
					list = list[:max(1, len(list)/2)]
					out["members"] = list
					out["has_more"] = true
					out["next_before"] = list[len(list)-1]["id"]
				} else if window.MaxChars > 1 {
					window.MaxChars = max(1, window.MaxChars/2)
					body, total, next := textWindow(p.Body, window)
					out["body"] = body
					out["body_truncated"] = next < total
					if next < total {
						out["next_offset"] = next
					}
				} else {
					return actool.Errorf("摘要元数据超出响应预算"), nil
				}
			}
			return jsonResult(out)
		})
}
