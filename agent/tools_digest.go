package agent

// cold-digest §6: graph_overview folding + the restore tools.
//
//	coldDigestOverview — builds the folded cold region for graph_overview:
//	  cold_digests (flat {id, body, member_count}) and cold_index (§6.2, the
//	  per-asset directory that collapses independent directions).
//	expand_digest(id)     — level-1 restore: a digest's member compact list.
//	expand_index(asset_id) — level-0 restore: the digests under one asset.

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

// coldDigestOverview returns the folded cold region for graph_overview: the flat
// digest bodies and the asset-grouped index (§6.1/§6.2).
func (t *ToolSet) coldDigestOverview() (digests, index []map[string]any, resultErr error) {
	ads, err := t.ts.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil, nil, err
	}
	ids := make([]int64, 0, len(ads))
	for _, d := range ads {
		ids = append(ids, d.ID)
	}
	allowed, allMembersByDigest, batchErr := t.digestAuthorizationBatch(t.ts, t.taskID, ids)
	if batchErr != nil {
		return nil, nil, batchErr
	}
	memByDigest := map[int64][]int64{}
	var allMembers []int64
	for _, d := range ads {
		if !allowed[d.ID] {
			continue
		}
		ms := allMembersByDigest[d.ID]
		memByDigest[d.ID] = ms
		allMembers = append(allMembers, ms...)
	}
	assetsByNode, assetsErr := t.ts.NodeAssets(allMembers)
	if assetsErr != nil {
		return nil, nil, assetsErr
	}

	digests = make([]map[string]any, 0, len(ads))
	for _, d := range ads {
		if _, allowed := memByDigest[d.ID]; !allowed {
			continue
		}
		var p struct {
			Body string `json:"body"`
		}
		if err := json.Unmarshal(d.Payload, &p); err != nil {
			return nil, nil, err
		}
		digests = append(digests, map[string]any{
			"id":              d.ID,
			"body":            firstLine(p.Body, 1200),
			"body_is_summary": true,
			"member_count":    len(memByDigest[d.ID]),
		})
	}

	// index (§6.2): asset → digests. Each digest lands in EXACTLY ONE bucket — its
	// representative asset = the asset anchored on the most of its members (mode;
	// tie-break lowest id). This is what makes the index converge: bucketing a digest
	// into every asset its members touch would duplicate it across dozens of buckets
	// and blow up the top-level count instead of shrinking it (asset_ids are fine-
	// grained — a real task has ~144 of them). A digest whose members anchor no asset
	// falls into the 0 bucket ("(未锚定资产)").
	byAsset := map[int64]map[int64]bool{} // asset id → set of digest ids
	assetSet := map[int64]bool{}
	for dID, ms := range memByDigest {
		counts := map[int64]int{}
		for _, m := range ms {
			for _, a := range assetsByNode[m] {
				counts[a]++
			}
		}
		rep, best := int64(0), 0
		for a, c := range counts {
			if c > best || (c == best && (rep == 0 || a < rep)) {
				rep, best = a, c
			}
		}
		if byAsset[rep] == nil {
			byAsset[rep] = map[int64]bool{}
		}
		byAsset[rep][dID] = true
		if rep != 0 {
			assetSet[rep] = true
		}
	}
	labels := map[int64]string{}
	if t.as != nil && len(assetSet) > 0 {
		ids := make([]int64, 0, len(assetSet))
		for a := range assetSet {
			ids = append(ids, a)
		}
		if assets, err := t.as.GetByIDs(ids); err == nil {
			for _, a := range assets {
				if v := assetValue(a); v != "" {
					labels[a.ID] = v
				}
			}
		}
	}
	index = make([]map[string]any, 0, len(byAsset))
	for a, dset := range byAsset {
		dids := make([]int64, 0, len(dset))
		for d := range dset {
			dids = append(dids, d)
		}
		sort.Slice(dids, func(i, j int) bool { return dids[i] < dids[j] })
		entry := map[string]any{"digest_ids": dids}
		if a == 0 {
			entry["asset"] = "(未锚定资产)"
		} else {
			entry["asset_id"] = a
			if l := labels[a]; l != "" {
				entry["asset"] = l
			} else {
				entry["asset"] = fmt.Sprintf("#%d", a)
			}
		}
		index = append(index, entry)
	}
	sort.Slice(index, func(i, j int) bool {
		return fmt.Sprint(index[i]["asset"]) < fmt.Sprint(index[j]["asset"])
	})
	return digests, index, nil
}

// activeDigestBodies returns [{id, body, member_count}] for a store's active
// digests — the folded cold region as flat bodies (§6.1). Shared by the current
// task overview and the read-only related-task overview (§2 cross-task reuse).
func (t *ToolSet) activeDigestBodies(store *db.ExplorationStore, ownerTaskID int64) []map[string]any {
	ads, err := store.ActiveDigests()
	if err != nil || len(ads) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(ads))
	for _, d := range ads {
		ids = append(ids, d.ID)
	}
	allowed, members, err := t.digestAuthorizationBatch(store, ownerTaskID, ids)
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(ads))
	for _, d := range ads {
		if !allowed[d.ID] {
			continue
		}
		var p struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(d.Payload, &p)
		out = append(out, map[string]any{"id": d.ID, "body": firstLine(p.Body, 1200), "body_is_summary": true, "member_count": len(members[d.ID])})
	}
	return out
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
	return writeTool("expand_digest",
		"展开一个 cold digest：返回它折叠的成员紧凑列表（id/summary/state/confidence），与概览 recent_facts/recent_done_intents 同形状。要某条完整细节/证据用 node_detail(member_id)。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id":    map[string]any{"type": "integer", "description": "digest 节点 id（来自概览 cold_digests / cold_index / covered_members）"},
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

// expandIndex returns the active digests under one asset (§6.2 level-0), each
// with its body + member count — so the planner can drill an asset directory down
// to its directions without reading every digest globally.
func (t *ToolSet) expandIndex() actool.CoreTool {
	return writeTool("expand_index", "按资产展开冷摘要索引（0为未锚定桶）。默认20，最大100；before续页，正文详情使用 expand_digest。",
		obj(map[string]any{"asset_id": idp("资产 ID，默认0"), "before": intp("next_before 续页"), "limit": intp("默认20，最大100")}),
		func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			if t.ts == nil {
				return actool.Errorf("缺少任务探索上下文"), nil
			}
			var in struct {
				AssetID int64 `json:"asset_id"`
				Before  int64 `json:"before"`
				Limit   int   `json:"limit"`
			}
			if err := decodeToolInput(raw, &in); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if in.Limit == 0 {
				in.Limit = 20
			}
			if in.AssetID < 0 || in.Before < 0 || in.Limit < 1 || in.Limit > 100 {
				return actool.Errorf("无效索引分页参数"), nil
			}
			page, err := t.ts.ToolDigestIndexPage(ctx, in.AssetID, in.Before, in.Limit)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			more := len(page.Digests) > in.Limit
			if more {
				page.Digests = page.Digests[:in.Limit]
			}
			rows := []map[string]any{}
			for _, d := range page.Digests {
				rows = append(rows, map[string]any{"id": d.ID, "kind": db.KindDigest, "state": "active", "body": d.Body, "member_count": d.MemberCount, "body_is_summary": true})
			}
			rows, cut := budgetRows(rows)
			more = more || cut
			out := map[string]any{"asset_id": in.AssetID, "digests": rows, "total": page.Total, "has_more": more}
			if more && len(rows) > 0 {
				out["next_before"] = rows[len(rows)-1]["id"]
			}
			return jsonResult(out)
		})
}
