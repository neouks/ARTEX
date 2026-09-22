package db

import (
	"context"
	"fmt"
)

type ActivitySearchHit struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Snippet string `json:"snippet"`
}

// ActivitySearch never returns full bodies. strpos treats %, _, quotes and
// backslashes literally; the request deadline also bounds the database scan.
func (s *ExplorationStore) ActivitySearch(ctx context.Context, f ActivitySessionFilter, query string, after, upper int64, limit int) ([]ActivitySearchHit, bool, int64, error) {
	cond, values := f.cond(2)
	args := append([]any{s.expID}, values...)
	where := "exploration_id=$1" + cond + " AND kind<>'usage'"
	if upper == 0 {
		if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM activity WHERE `+where, args...).Scan(&upper); err != nil {
			return nil, false, 0, err
		}
	}
	bind := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	a, u, q, n := bind(after), bind(upper), bind(query), bind(limit+1)
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,substring(body FROM greatest(1,pos-60) FOR 240) FROM (
 SELECT id,kind,body,strpos(lower(body),lower(`+q+`)) AS pos FROM (
 SELECT id,kind,COALESCE(NULLIF(detail,''),summary,'') AS body FROM activity WHERE `+where+` AND id>`+a+` AND id<=`+u+`
 ) bodies) matches WHERE pos>0 ORDER BY id LIMIT `+n, args...)
	if err != nil {
		return nil, false, upper, err
	}
	defer rows.Close()
	hits := []ActivitySearchHit{}
	for rows.Next() {
		var hit ActivitySearchHit
		if err := rows.Scan(&hit.ID, &hit.Kind, &hit.Snippet); err != nil {
			return nil, false, upper, err
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, false, upper, err
	}
	more := len(hits) > limit
	if more {
		hits = hits[:limit]
	}
	return hits, more, upper, nil
}
