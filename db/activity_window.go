package db

import (
	"database/sql"
	"errors"
	"fmt"
)

var ErrActivityAnchor = errors.New("source activity unavailable")

type ActivityWindow struct {
	Items    []Activity
	HasOlder bool
	HasNewer bool
}

// ActivityWindow reads a contiguous session window. When anchoring, the entire
// interval between the invocation and its result is retained, including parallel
// calls. A reused provider call ID is bounded by its next invocation.
func (s *ExplorationStore) ActivityWindow(f ActivitySessionFilter, anchor, after int64, limit int) (ActivityWindow, error) {
	out := ActivityWindow{Items: []Activity{}}
	cond, args := f.cond(2)
	args = append([]any{s.expID}, args...)
	where := "exploration_id=$1" + cond
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	low, high := after, int64(0)
	if anchor > 0 {
		var call, kind string
		originalAnchor := anchor
		var worker string
		var node, seg int64
		err := s.db.QueryRow(`SELECT kind,COALESCE(tool_use_id,''),COALESCE(worker,''),COALESCE(node_id,0),COALESCE(main_seg,0) FROM activity WHERE `+where+` AND id=`+bind(anchor), args...).Scan(&kind, &call, &worker, &node, &seg)
		if err == sql.ErrNoRows {
			return out, ErrActivityAnchor
		}
		if err != nil {
			return out, err
		}
		if kind == "tool_result" && call != "" {
			// The closest preceding invocation owns this result; never cross sessions.
			args = args[:len(args)-1]
			var use int64
			err = s.db.QueryRow(`SELECT COALESCE(MAX(id),0) FROM activity WHERE `+where+` AND kind='tool_use' AND id<`+bind(anchor)+` AND tool_use_id=`+bind(call)+` AND COALESCE(worker,'')=`+bind(worker)+` AND COALESCE(node_id,0)=`+bind(node)+` AND COALESCE(main_seg,0)=`+bind(seg), args...).Scan(&use)
			if err != nil {
				return out, err
			}
			if use > 0 {
				anchor = use
				kind = "tool_use"
			}
		}
		pair := anchor
		if kind == "tool_use" && call != "" {
			err = s.db.QueryRow(`SELECT COALESCE(MIN(r.id),$2) FROM activity r WHERE r.exploration_id=$1 AND r.id>$2 AND r.kind='tool_result' AND r.tool_use_id=$3
AND COALESCE(r.worker,'')=$4 AND COALESCE(r.node_id,0)=$5 AND COALESCE(r.main_seg,0)=$6
AND NOT EXISTS(SELECT 1 FROM activity n WHERE n.exploration_id=r.exploration_id AND n.id>$2 AND n.id<r.id AND n.kind='tool_use' AND n.tool_use_id=$3 AND COALESCE(n.worker,'')=$4 AND COALESCE(n.node_id,0)=$5 AND COALESCE(n.main_seg,0)=$6)`, s.expID, anchor, call, worker, node, seg).Scan(&pair)
			if err != nil {
				return out, err
			}
		}
		pair = max(pair, originalAnchor)
		low = anchor
		high = pair
		// Neighborhood bounds are selected without loading any detail bodies.
		cond, values := f.cond(2)
		args = append([]any{s.expID}, values...)
		where = "exploration_id=$1" + cond
		a := bind(anchor)
		n := bind(max(1, limit/2))
		err = s.db.QueryRow(`SELECT COALESCE(MIN(id),`+a+`) FROM (SELECT id FROM activity WHERE `+where+` AND id<=`+a+` ORDER BY id DESC LIMIT `+n+`) x`, args...).Scan(&low)
		if err != nil {
			return out, err
		}
		args = append([]any{s.expID}, values...)
		p := bind(pair)
		n = bind(max(1, limit/2))
		err = s.db.QueryRow(`SELECT COALESCE(MAX(id),`+p+`) FROM (SELECT id FROM activity WHERE `+where+` AND id>=`+p+` ORDER BY id LIMIT `+n+`) x`, args...).Scan(&high)
		if err != nil {
			return out, err
		}
	}
	// Build fresh bindings: PostgreSQL rejects unused parameters in a query.
	cond, values := f.cond(2)
	args = append([]any{s.expID}, values...)
	where = "exploration_id=$1" + cond
	lo := bind(low)
	rangeSQL := " AND id>" + lo
	if anchor > 0 {
		rangeSQL = " AND id>=" + lo + " AND id<=" + bind(high)
	}
	tail := " ORDER BY id"
	if anchor == 0 {
		tail += " LIMIT " + bind(limit+1)
	}
	rows, err := s.db.Query(`SELECT id,node_id,COALESCE(worker,''),COALESCE(kind,''),COALESCE(tool,''),COALESCE(tool_use_id,''),is_error,COALESCE(summary,''),metadata,created_at,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,main_seg FROM activity WHERE `+where+rangeSQL+tail, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var a Activity
		if err := rows.Scan(&a.ID, &a.NodeID, &a.Worker, &a.Kind, &a.Tool, &a.ToolUseID, &a.IsError, &a.Summary, &a.Metadata, &a.CreatedAt, &a.InputTokens, &a.OutputTokens, &a.CacheReadTokens, &a.CacheWriteTokens, &a.MainSeg); err != nil {
			rows.Close()
			return out, err
		}
		out.Items = append(out.Items, a)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if anchor > 0 {
		found := false
		for _, item := range out.Items {
			if item.ID == anchor {
				found = true
				break
			}
		}
		if !found {
			return ActivityWindow{}, ErrActivityAnchor
		}
	}
	if anchor == 0 && len(out.Items) > limit {
		out.Items = out.Items[:limit]
	}
	if len(out.Items) > 0 {
		low = out.Items[0].ID
		high = out.Items[len(out.Items)-1].ID
	} else {
		high = low
	}
	cond, values = f.cond(2)
	args = append([]any{s.expID}, values...)
	where = "exploration_id=$1" + cond
	lo = bind(low)
	hi := bind(high)
	err = s.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM activity WHERE `+where+` AND id<`+lo+`),EXISTS(SELECT 1 FROM activity WHERE `+where+` AND id>`+hi+`)`, args...).Scan(&out.HasOlder, &out.HasNewer)
	return out, err
}
