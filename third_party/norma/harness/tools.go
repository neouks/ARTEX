package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"runtime/debug"
	"slices"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/permission"
	"github.com/Autumn-27/norma/tool"
)

// rawInputTool is the optional opt-out from schema validation. Discovered by
// assertion rather than declared on tool.CoreTool, so hosts implementing that
// interface are unaffected.
type rawInputTool interface{ AcceptsRawInput() bool }

func acceptsRawInput(t tool.CoreTool) bool {
	r, ok := t.(rawInputTool)
	return ok && r.AcceptsRawInput()
}

// execOne resolves, permission-checks, and runs a single tool call, returning a
// tool_result block and any extra messages the tool injects after its result
// (Result.Extra — e.g. the Skill tool's instructions message). It never panics;
// failures become error results. It is safe to call from the streaming
// executor's goroutines (read-only access to loop config; permission/hook
// callbacks are the host's responsibility to make safe).
func (l *loop) execOne(toolCtx context.Context, settling bool, use llm.ContentBlock, emitProgress func(tool.ProgressInfo)) (result llm.ContentBlock, extra []llm.Message) {
	defer func() {
		if recover() != nil {
			// Do not put panic values, inputs or stack traces in model-visible output.
			log.Printf("[tool] panic in %q\n%s", use.Name, debug.Stack())
			result = llm.ToolResultText(use.ID, "Error: tool execution failed internally; effects may be partial. Do not automatically retry; verify the operation outcome first.", true)
			extra = nil
		}
	}()
	if toolCtx.Err() != nil {
		return llm.ToolResultText(use.ID, "Error: tool execution cancelled", true), nil
	}
	if settling && l.in.Settlement != nil && slices.Contains(l.in.Settlement.DisabledTools, use.Name) {
		return llm.ToolResultText(use.ID, "Error: tool is disabled during settlement", true), nil
	}
	if slices.Contains(l.in.DeferredTools, use.Name) && (l.in.UnlockSet == nil || !l.in.UnlockSet.Has(use.Name)) {
		return llm.ToolResultText(use.ID, "Error: deferred tool is not unlocked in this session", true), nil
	}
	t, ok := l.in.Tools.Get(use.Name)
	if !ok {
		schemas := filterToolSchemas(l.in.Tools.Schemas(), l.in.DeferredTools)
		if settling && l.in.Settlement != nil {
			schemas = filterToolSchemas(schemas, l.in.Settlement.DisabledTools)
		}
		return llm.ToolResultText(use.ID, fmt.Sprintf("Error: unknown tool %q", use.Name)+toolNameHint(use.Name, schemas), true), nil
	}
	input := tool.ApplyInputDefaults(use.Input, t.InputSchema())

	// Schema validation (FR-04.5).
	//
	// A tool may opt out by reporting AcceptsRawInput: it parses its own
	// arguments and would rather repair a malformed call than have it rejected
	// here, where the only possible answer is a generic error. The schema is
	// still advertised to the model either way — see tool.Spec.RawInput.
	if !acceptsRawInput(t) {
		if err := tool.ValidateInput(t.InputSchema(), input); err != nil {
			return llm.ToolResultText(use.ID, "Error: invalid tool input: "+err.Error(), true), nil
		}
	}

	mode := l.in.PermissionMode
	if l.in.PermissionModeFunc != nil {
		mode = l.in.PermissionModeFunc()
	}
	pc := permission.Context{
		Mode:       mode,
		WorkingDir: l.in.WorkingDir,
		Allowed:    l.in.Allowed,
		Disallowed: l.in.Disallowed,
	}
	dec := permission.Evaluate(l.ctx, permission.Request{
		ToolName:     use.Name,
		Input:        input,
		IsReadOnly:   t.IsReadOnly(input),
		ToolDecision: t.CheckPermissions(l.ctx, input, pc),
		Ctx:          pc,
		Ask:          l.in.CanUseTool,
	})
	if dec.Behavior != permission.Allow {
		msg := dec.Message
		if msg == "" {
			msg = "denied"
		}
		return llm.ToolResultText(use.ID, "Tool call was not permitted: "+msg, true), nil
	}
	if len(dec.UpdatedInput) > 0 {
		input = tool.ApplyInputDefaults(dec.UpdatedInput, t.InputSchema())
		if err := tool.ValidateInput(t.InputSchema(), input); err != nil {
			return llm.ToolResultText(use.ID, "Error: invalid permission-updated input: "+err.Error(), true), nil
		}
	}

	// Pre-tool hook.
	// The access wrapper is not the operation. Its resolved target is checked and
	// audited below with its actual name, effective defaults and parameters.
	if l.in.Hooks != nil && use.Name != tool.ExecuteExtraToolName {
		if block, msg, updated := l.in.Hooks.PreToolUse(l.ctx, use.Name, input); block {
			return llm.ToolResultText(use.ID, "Blocked by hook: "+msg, true), nil
		} else if len(updated) > 0 {
			input = tool.ApplyInputDefaults(updated, t.InputSchema())
			if err := tool.ValidateInput(t.InputSchema(), input); err != nil {
				return llm.ToolResultText(use.ID, "Error: invalid hook-updated input: "+err.Error(), true), nil
			}
		}
	}

	tc := &tool.ToolContext{
		ToolUseID: use.ID,
		ExecuteTool: func(_ context.Context, name string, input json.RawMessage) (tool.Result, error) {
			if !slices.Contains(l.in.DeferredTools, name) || name == tool.ExecuteExtraToolName || name == tool.SearchExtraToolsName {
				return tool.Errorf("Error: nested execution requires a registered deferred target"), nil
			}
			block, messages := l.execOne(toolCtx, settling, llm.ContentBlock{ID: use.ID, Name: name, Input: input}, emitProgress)
			return tool.Result{Content: block.Content, IsError: block.IsError, Extra: messages}, nil
		},
		WorkingDir:     l.in.WorkingDir,
		AgentID:        l.in.AgentID,
		Emit:           emitProgress,
		OutputDir:      l.in.ToolOutputDir,
		MaxOutputChars: l.in.MaxToolOutputChars,
		Tasks:          l.in.Tasks,
		Env:            l.in.BashEnv,
		ShellProfile:   l.in.ShellProfile,
	}
	if use.Name != tool.ExecuteExtraToolName {
		// Only the access wrapper may dispatch once; target tools cannot recurse
		// or replace the server-owned execution context with a fresh context.
		tc.ExecuteTool = nil
	}
	// toolCtx carries the run's MaxDuration deadline, so a tool that overruns the
	// wall-clock budget is interrupted here (the turn loop then enters wrap-up). During
	// the wrap-up phase toolCtx is the live parent ctx, so settlement tools run freely.
	res, err := t.Call(toolCtx, input, tc)
	if err != nil {
		res = tool.Errorf("Error: " + err.Error())
	}

	if l.in.Hooks != nil && use.Name != tool.ExecuteExtraToolName {
		raw, _ := json.Marshal(res.Flatten())
		l.in.Hooks.PostToolUse(l.ctx, use.Name, input, raw, res.IsError)
	}

	content := res.Content
	if len(content) == 0 {
		content = []llm.ContentBlock{llm.TextBlock("(no output)")}
	}
	return llm.ContentBlock{Type: llm.BlockToolResult, ToolUseID: use.ID, Content: content, IsError: res.IsError}, res.Extra
}
