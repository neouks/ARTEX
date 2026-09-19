package db

import (
	"context"
	"database/sql"
	"fmt"
)

// Never load evidence bodies for collapsed rows or neighbouring nodes.
const broadcastNodeCols = `id, kind, jsonb_build_object('summary',left(COALESCE(NULLIF(payload->>'name',''),NULLIF(payload->>'summary',''),NULLIF(payload->>'text',''),NULLIF(payload->>'description',''),NULLIF(payload->>'body',''),''),500)), priority, state, left(COALESCE(origin,''),160), '', '', created_at`
const BroadcastBodyPage = 16000
const BroadcastEdgePage = 50

type BroadcastDetail struct {
	Node        *Node
	Payload     string
	PayloadNext int
	Edges       []Edge
	EdgesNext   int
	Refs        []*Node
}

// BroadcastNode reads one body chunk and one bounded relation page. All reads
// are task-scoped and canceled with the HTTP request; a hub cannot expand the
// entire graph in one response.
func (s *ExplorationStore) BroadcastNode(ctx context.Context, id int64, bodyOffset, edgeOffset int) (*BroadcastDetail, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+broadcastNodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND id=$2`, s.expID, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := &BroadcastDetail{Node: n, PayloadNext: -1, EdgesNext: -1, Edges: []Edge{}}
	if bodyOffset >= 0 {
		var total int
		if err = s.db.QueryRowContext(ctx, `SELECT substring(payload::text FROM $3+1 FOR $4),char_length(payload::text) FROM exploration_nodes WHERE exploration_id=$1 AND id=$2`, s.expID, id, bodyOffset, BroadcastBodyPage).Scan(&out.Payload, &total); err != nil {
			return nil, err
		}
		if bodyOffset+BroadcastBodyPage < total {
			out.PayloadNext = bodyOffset + BroadcastBodyPage
		}
	}
	if edgeOffset >= 0 {
		rows, err := s.db.QueryContext(ctx, `SELECT src_id,rel,dst_id FROM exploration_edges WHERE exploration_id=$1 AND (src_id=$2 OR dst_id=$2) ORDER BY src_id,dst_id,rel LIMIT $3 OFFSET $4`, s.expID, id, BroadcastEdgePage+1, edgeOffset)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var e Edge
			if err = rows.Scan(&e.From, &e.Rel, &e.To); err != nil {
				rows.Close()
				return nil, err
			}
			out.Edges = append(out.Edges, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		if len(out.Edges) > BroadcastEdgePage {
			out.Edges = out.Edges[:BroadcastEdgePage]
			out.EdgesNext = edgeOffset + BroadcastEdgePage
		}
		args := []any{s.expID}
		seen := map[int64]bool{}
		marks := ""
		for _, e := range out.Edges {
			for _, nid := range []int64{e.From, e.To} {
				if seen[nid] {
					continue
				}
				seen[nid] = true
				args = append(args, nid)
				if marks != "" {
					marks += ","
				}
				marks += fmt.Sprintf("$%d", len(args))
			}
		}
		if marks != "" {
			rows, err = s.db.QueryContext(ctx, `SELECT `+broadcastNodeCols+` FROM exploration_nodes WHERE exploration_id=$1 AND id IN (`+marks+`)`, args...)
			if err != nil {
				return nil, err
			}
			out.Refs, err = scanNodes(rows)
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
