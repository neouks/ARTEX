package db

import (
	"context"
	"fmt"
	"strings"
)

// MainDispatchFeedback ties a successful selection to its original conversation.
// The queue lock prevents a Worker claim between permission and subscription.
func (s *ExplorationStore) MainDispatchFeedback(ctx context.Context, id int64, seg int, grant bool) error {
	tx, err := s.beginQueueContext(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM exploration_nodes WHERE id=$1 AND exploration_id=$2 AND kind='intent' AND payload->>'cancelled_by_user' IS DISTINCT FROM 'true' FOR UPDATE`, id, s.expID).Scan(&state); err != nil {
		return err
	}
	if state != "open" && state != "running" {
		return ErrIntentStateConflict
	}
	if grant && state == "open" {
		result, e := tx.ExecContext(ctx, mainIntentDispatchSQL, id, s.expID)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrIntentStateConflict
		}
	}
	if seg < 0 {
		return fmt.Errorf("invalid main session")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO worker_feedback(exploration_id,task_id,intent_id,main_seg,after_activity)
 SELECT $1,t.id,$2,$3,CASE WHEN $4='running' THEN COALESCE((SELECT after_activity FROM worker_feedback_run WHERE intent_id=$2),(SELECT max(id) FROM activity WHERE exploration_id=$1 AND node_id=$2),0) ELSE COALESCE((SELECT max(id) FROM activity WHERE exploration_id=$1 AND node_id=$2),0) END
 FROM tasks t WHERE t.exploration_id=$1
 ON CONFLICT(exploration_id,intent_id,main_seg) WHERE NOT terminal DO NOTHING`, s.expID, id, seg, state)
	if err != nil {
		return err
	}
	return tx.Commit()
}

type WorkerFeedbackDelivery struct {
	TaskID   string
	Activity Activity
}

// UnpublishedWorkerFeedback is a bounded outbox read; replay uses stable activity
// IDs so reconnect compensation and duplicate SSE delivery are harmless.
func (d *DB) UnpublishedWorkerFeedback(ctx context.Context) ([]WorkerFeedbackDelivery, error) {
	rows, err := d.QueryContext(ctx, `SELECT o.task_id::text,a.id,COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.summary,''),a.metadata,a.main_seg,a.is_error,a.created_at FROM worker_feedback_delivery o JOIN activity a ON a.id=o.activity_id WHERE NOT o.published ORDER BY a.id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorkerFeedbackDelivery
	for rows.Next() {
		var r WorkerFeedbackDelivery
		a := &r.Activity
		if err = rows.Scan(&r.TaskID, &a.ID, &a.Worker, &a.Kind, &a.Summary, &a.Metadata, &a.MainSeg, &a.IsError, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (d *DB) MarkWorkerFeedbackPublished(ctx context.Context, id int64) error {
	_, err := d.ExecContext(ctx, `UPDATE worker_feedback_delivery SET published=true WHERE activity_id=$1`, id)
	return err
}

// WorkerFeedbackContext is refreshed at each user turn, including transcript/noa
// restore. It does not modify the raw transcript or schedule an agent turn.
func (s *ExplorationStore) WorkerFeedbackContext(ctx context.Context, seg int) (string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT left(COALESCE(detail,summary,''),12000) FROM activity WHERE exploration_id=$1 AND worker='mainagent' AND main_seg=$2 AND metadata ? 'worker_feedback' ORDER BY id DESC LIMIT 20`, s.expID, seg)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var b strings.Builder
	budget := 12000
	for rows.Next() {
		var body string
		if err = rows.Scan(&body); err != nil {
			return "", err
		}
		r := []rune(body)
		if len(r) > budget {
			r = r[:budget]
		}
		b.WriteString(string(r))
		b.WriteString("\n\n")
		budget -= len(r)
		if budget <= 0 {
			b.WriteString("（更多结果请读取对应 Worker 会话）")
			break
		}
	}
	return b.String(), rows.Err()
}

// WorkerFeedbackReplay uses its own cursor: a delayed outbox notification must
// remain replayable even when ordinary activity has advanced the SSE cursor.
func (s *ExplorationStore) WorkerFeedbackReplay(ctx context.Context, after, before int64) ([]Activity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT a.id,COALESCE(a.worker,''),COALESCE(a.kind,''),COALESCE(a.summary,''),a.metadata,a.main_seg,a.is_error,a.created_at FROM worker_feedback_delivery o JOIN activity a ON a.id=o.activity_id WHERE o.task_id=(SELECT id FROM tasks WHERE exploration_id=$1) AND a.exploration_id=$1 AND a.id>$2 AND a.id<=$3 ORDER BY a.id LIMIT 100`, s.expID, after, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activity
	for rows.Next() {
		var a Activity
		if err = rows.Scan(&a.ID, &a.Worker, &a.Kind, &a.Summary, &a.Metadata, &a.MainSeg, &a.IsError, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
