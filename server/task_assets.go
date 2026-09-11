package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

const maxTaskAssetRequestBytes = 512 << 10

func (s *Server) updateTaskAssetTemplate(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, 404, "task not found")
		return
	}
	var req struct {
		Template string `json:"asset_approval_template"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskAssetRequestBytes)
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	id, _ := parseTaskID(task.ID)
	if err := s.m.pg.SetAssetApprovalTemplate(id, req.Template); err != nil {
		writeTaskAssetError(w, err)
		return
	}
	task.updateLifecycle(func(state *taskLifecycleState) { state.AssetApprovalTemplate = req.Template })
	task.Notify()
	writeJSON(w, 200, map[string]any{"asset_approval_template": req.Template})
}

func writeTaskAssetError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		return
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "资产查询超时，请缩小查询范围后重试")
	case errors.Is(err, db.ErrTaskAssetInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, db.ErrTaskAssetBlocked), errors.Is(err, db.ErrTaskAssetNotApproved):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, db.ErrTaskAssetTaskNotFound), errors.Is(err, db.ErrTaskAssetAssetNotFound):
		writeErr(w, http.StatusNotFound, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func taskAssetActor(_ *http.Request) string {
	return "ARTEX"
}

func (s *Server) listTaskAssetApprovals(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	as := s.m.Assets()
	if as == nil {
		writeErr(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	as = as.WithReadContext(ctx)
	id, _ := parseTaskID(task.ID)
	if groupBy := r.URL.Query().Get("group_by"); groupBy != "" && groupBy != "host" {
		writeErr(w, 400, "group_by 必须是 host 或省略")
		return
	}
	if r.URL.Query().Get("group_by") == "host" {
		items, err := as.ListTaskAssetApprovalGroups(id)
		if err != nil {
			writeTaskAssetError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	items, err := as.ListTaskAssetApprovals(id)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) updateTaskAssetApprovals(w http.ResponseWriter, r *http.Request, approve bool) {
	operation := "revoke"
	if approve {
		operation = "approve"
	}
	s.mutateTaskAssetApprovals(w, r, operation)
}

func (s *Server) mutateTaskAssetApprovals(w http.ResponseWriter, r *http.Request, operation string) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	var req struct {
		AssetIDs  []int64  `json:"asset_ids"`
		GroupKeys []string `json:"group_keys"`
		Reason    string   `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskAssetRequestBytes)
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := parseTaskID(task.ID)
	actor := taskAssetActor(r)
	var resolvedIDs []int64
	var err error
	if operation == "approve" {
		resolvedIDs, err = s.m.Assets().ApproveTaskAssetsResolved(id, req.AssetIDs, actor, req.Reason, req.GroupKeys...)
	} else if operation == "block" {
		resolvedIDs, err = s.m.Assets().BlockTaskAssetsResolved(id, req.AssetIDs, actor, req.Reason, req.GroupKeys...)
	} else {
		resolvedIDs, err = s.m.Assets().RevokeTaskAssetsResolved(id, req.AssetIDs, actor, req.Reason, req.GroupKeys...)
	}
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	req.AssetIDs = resolvedIDs
	if operation != "approve" {
		s.engine.CancelWorkersForAssets(id, req.AssetIDs)
	}
	task.Notify()
	all, err := s.m.Assets().ListTaskAssetApprovals(id)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	wanted := make(map[int64]bool, len(req.AssetIDs))
	for _, assetID := range req.AssetIDs {
		wanted[assetID] = true
	}
	items := make([]db.TaskAssetApproval, 0, len(wanted))
	for _, item := range all {
		if wanted[item.AssetID] {
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"asset_ids":      req.AssetIDs,
		"approval_state": map[string]string{"approve": db.ApprovalApproved, "revoke": db.ApprovalRevoked, "block": db.ApprovalBlocked}[operation],
		"items":          items,
	})
}

func (s *Server) attachTaskAssets(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskAssetRequestBytes)
	var request struct {
		AssetIDs      []int64            `json:"asset_ids"`
		SourceSummary string             `json:"source_summary"`
		Scope         companyScopeInputs `json:"scope"`
	}
	if err := decode(r, &request); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求正文过大")
		} else {
			writeErr(w, http.StatusBadRequest, err.Error())
		}
		return
	}
	request.SourceSummary = strings.TrimSpace(request.SourceSummary)
	taskID, _ := parseTaskID(task.ID)
	if request.Scope != nil && len(request.AssetIDs) > 0 {
		writeErr(w, http.StatusBadRequest, "scope 与 asset_ids 不能同时提交")
		return
	}
	if request.Scope != nil {
		mutation, err := s.m.Assets().RegisterTaskAssetScopes(taskID, request.Scope)
		if err != nil {
			writeTaskAssetError(w, err)
			return
		}
		task.Notify()
		writeJSON(w, http.StatusOK, mutation)
		return
	}
	mutation, err := s.m.Assets().AttachAssetsToTask(taskID, request.AssetIDs, request.SourceSummary)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	task.Notify()
	writeJSON(w, http.StatusOK, mutation)
}

func (s *Server) detachTaskAsset(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	assetID, ok := pathInt(r, "assetID")
	if !ok {
		writeErr(w, http.StatusBadRequest, "bad asset id")
		return
	}
	taskID, _ := parseTaskID(task.ID)
	detached, err := s.m.Assets().DetachAssetFromTask(taskID, assetID)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	if !detached {
		writeErr(w, http.StatusNotFound, "asset is not associated with this task")
		return
	}
	s.engine.CancelWorkersForAssets(taskID, []int64{assetID})
	task.Notify()
	writeJSON(w, http.StatusOK, map[string]any{"detached": assetID})
}

func (s *Server) taskIntentAssets(w http.ResponseWriter, r *http.Request) {
	task, ok := s.m.Task(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "task not found")
		return
	}
	taskID, _ := parseTaskID(task.ID)
	q := r.URL.Query()
	before := int64(atoiDefault(q.Get("before"), 0))
	limit := 0
	if q.Get("limit") != "" {
		limit = min(max(atoiDefault(q.Get("limit"), 300), 1), 500)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	assets, err := s.m.Assets().WithReadContext(ctx).IntentAssetsPage(taskID, before, limit)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}
