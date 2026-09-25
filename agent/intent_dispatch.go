package agent

import (
	"context"
	"encoding/json"
	"fmt"
	actool "github.com/Autumn-27/norma/tool"
)

type IntentDispatchResult struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}
type IntentDispatcher func(context.Context, []int64) ([]IntentDispatchResult, error)
type intentDispatcherKey struct{}

func WithIntentDispatcher(ctx context.Context, dispatch IntentDispatcher) context.Context {
	return context.WithValue(ctx, intentDispatcherKey{}, dispatch)
}
func (t *ToolSet) dispatch(ctx context.Context, ids []int64) ([]IntentDispatchResult, error) {
	run := RunInfoFrom(ctx)
	fn, _ := ctx.Value(intentDispatcherKey{}).(IntentDispatcher)
	if run.AgentKey != "mainagent" || run.IntentID != 0 || t.taskID <= 0 || run.TaskID != t.taskID || fn == nil {
		return nil, fmt.Errorf("仅任务主 Agent 可下发意图")
	}
	results, err := fn(ctx, ids)
	if err == nil {
		recordMainDispatch(ctx, results)
	}
	return results, err
}
func (t *ToolSet) dispatchIntents() actool.CoreTool {
	return writeTool("dispatch_intents", "将当前任务已有的待执行意图下发给 Worker。成功后结束本轮，Worker 总结自动返回原会话；不要 sleep 或轮询等待。仅在用户要求执行时使用；可下发待审批资产意图；仍遵守封禁、撤回、任务并发及取消状态。", obj(map[string]any{"intent_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 50, "items": map[string]any{"type": "integer", "minimum": 1}}}, "intent_ids"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
		var req struct {
			IDs []int64 `json:"intent_ids"`
		}
		if err := json.Unmarshal(in, &req); err != nil {
			return actool.Errorf(err.Error()), nil
		}
		results, err := t.dispatch(ctx, req.IDs)
		if err != nil {
			return actool.Errorf(err.Error()), nil
		}
		return jsonResult(map[string]any{"results": results})
	})
}

// The host serializes the whole create+admit operation against mode changes and
// worker claims. Only this trusted runtime scope may mark new intents released.
type IntentSubmission func(context.Context, func(context.Context) (actool.Result, error)) (actool.Result, error)
type intentSubmissionKey struct{}
type intentSubmissionLockedKey struct{}
type createdIntentKey struct{}

func WithIntentSubmission(ctx context.Context, fn IntentSubmission) context.Context {
	return context.WithValue(ctx, intentSubmissionKey{}, fn)
}
func CreatedDispatchIntents(ctx context.Context) []int64 {
	ids, _ := ctx.Value(createdIntentKey{}).([]int64)
	return ids
}

type submissionTool struct{ actool.CoreTool }

func (t submissionTool) Call(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
	fn, _ := ctx.Value(intentSubmissionKey{}).(IntentSubmission)
	if fn == nil || RunInfoFrom(ctx).AgentKey != "mainagent" {
		return t.CoreTool.Call(ctx, in, tc)
	}
	return fn(ctx, func(locked context.Context) (actool.Result, error) {
		return t.CoreTool.Call(context.WithValue(locked, intentSubmissionLockedKey{}, true), in, tc)
	})
}
