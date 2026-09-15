package db

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxFindingDeletionReason = 2000

type FindingDeletionFeedback struct {
	ID        int64     `json:"id"`
	FindingID int64     `json:"finding_id"`
	TaskID    *int64    `json:"task_id,omitempty"`
	Title     string    `json:"title"`
	VulnClass string    `json:"vulnclass"`
	Reason    string    `json:"reason"`
	DeletedAt time.Time `json:"deleted_at"`
}

func NormalizeFindingDeletionReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > MaxFindingDeletionReason {
		return "", fmt.Errorf("删除原因最多2000字符")
	}
	return reason, nil
}

func (d *DB) DeleteFindingWithFeedback(id int64, reason string) (*FindingDeletionFeedback, error) {
	reason, err := NormalizeFindingDeletionReason(reason)
	if err != nil {
		return nil, err
	}
	_, feedback, err := d.deleteFinding(id, &reason)
	if err != nil {
		return nil, err
	}
	return feedback, nil
}

func (s *ExplorationStore) FindingDeletionFeedback(before int64, limit int) ([]FindingDeletionFeedback, error) {
	if before < 0 || limit < 1 || limit > 50 {
		return nil, fmt.Errorf("before 须非负，limit 须为1..50")
	}
	rows, err := s.db.Query(`SELECT f.id,f.finding_id,f.task_id,f.title,f.vulnclass,f.reason,f.deleted_at
 FROM finding_deletion_feedback f JOIN tasks t ON t.id=f.task_id
 WHERE t.exploration_id=$1 AND t.deleted_at IS NULL AND ($2::bigint=0 OR f.id<$2)
 ORDER BY f.id DESC LIMIT $3`, s.expID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []FindingDeletionFeedback{}
	for rows.Next() {
		var f FindingDeletionFeedback
		if err := rows.Scan(&f.ID, &f.FindingID, &f.TaskID, &f.Title, &f.VulnClass, &f.Reason, &f.DeletedAt); err != nil {
			return nil, err
		}
		result = append(result, f)
	}
	return result, rows.Err()
}

func (s *ExplorationStore) FindingDeletionFeedbackField(id int64, field string) (string, error) {
	if field == "" {
		field = "reason"
	}
	if field != "reason" && field != "title" && field != "vulnclass" {
		return "", fmt.Errorf("未知反馈字段")
	}
	var value string
	err := s.db.QueryRow(`SELECT f.`+field+` FROM finding_deletion_feedback f JOIN tasks t ON t.id=f.task_id WHERE t.exploration_id=$1 AND t.deleted_at IS NULL AND f.id=$2`, s.expID, id).Scan(&value)
	return value, err
}
