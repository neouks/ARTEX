package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Autumn-27/artex/db"
)

const maxTaskAssetRequestBytes = 512 << 10

func writeTaskAssetError(w http.ResponseWriter, err error) {
	switch {
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
	id, _ := parseTaskID(task.ID)
	items, err := s.m.Assets().ListTaskAssetApprovals(id)
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
		AssetIDs []int64 `json:"asset_ids"`
		Reason   string  `json:"reason"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTaskAssetRequestBytes)
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := parseTaskID(task.ID)
	actor := taskAssetActor(r)
	var err error
	if operation == "approve" {
		err = s.m.Assets().ApproveTaskAssets(id, req.AssetIDs, actor, req.Reason)
	} else if operation == "block" {
		err = s.m.Assets().BlockTaskAssets(id, req.AssetIDs, actor, req.Reason)
	} else {
		err = s.m.Assets().RevokeTaskAssets(id, req.AssetIDs, actor, req.Reason)
	}
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
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
	assets, err := s.m.Assets().IntentAssets(taskID)
	if err != nil {
		writeTaskAssetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assets": assets})
}
