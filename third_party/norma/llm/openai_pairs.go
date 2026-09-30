package llm

import "log"

const reasoningElidedPlaceholder = "(reasoning elided by context compaction)"

// Keep the local batch-boundary repair, then apply the v0.4.2 thinking backstop
// to the request projection only. Never replace real reasoning or mutate history.
func sanitizeOpenAIMessages(messages []oaMessage, thinking bool) []oaMessage {
	out := pairOpenAIMessages(messages)
	if !thinking {
		return out
	}
	filled := 0
	for i := range out {
		if out[i].Role == "assistant" && len(out[i].ToolCalls) > 0 && out[i].ReasoningContent == "" {
			out[i].ReasoningContent = reasoningElidedPlaceholder
			filled++
		}
	}
	if filled > 0 {
		log.Printf("[openai] repaired request missing reasoning: tool_batches=%d", filled)
	}
	return out
}

// pairOpenAIMessages validates the serialized protocol, after all context
// projections. Never mutate shared history or move results across message turns.
func pairOpenAIMessages(messages []oaMessage) []oaMessage {
	out := make([]oaMessage, 0, len(messages))
	missing, duplicateCalls, invalidCalls, orphanResults, duplicateResults := 0, 0, 0, 0, 0
	for i := 0; i < len(messages); {
		m := messages[i]
		if m.Role == "tool" {
			orphanResults++
			i++
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			out = append(out, m)
			i++
			continue
		}
		end := i + 1
		for end < len(messages) && messages[end].Role == "tool" {
			end++
		}
		available := make(map[string]bool)
		for j := i + 1; j < end; j++ {
			if messages[j].ToolCallID != "" {
				available[messages[j].ToolCallID] = true
			}
		}
		seen := make(map[string]bool)
		retained := make(map[string]bool)
		calls := make([]oaToolCall, 0, len(m.ToolCalls))
		for _, call := range m.ToolCalls {
			if call.ID == "" {
				invalidCalls++
				continue
			}
			if seen[call.ID] {
				duplicateCalls++
				continue
			}
			seen[call.ID] = true
			if !available[call.ID] {
				missing++
				continue
			}
			calls = append(calls, call)
			retained[call.ID] = true
		}
		m.ToolCalls = calls
		if len(calls) > 0 || m.Content != "" || m.ReasoningContent != "" {
			if len(calls) == 0 && m.Content == "" {
				m.Content = "…"
			}
			out = append(out, m)
		}
		emitted := make(map[string]bool)
		for j := i + 1; j < end; j++ {
			result := messages[j]
			if !retained[result.ToolCallID] {
				orphanResults++
				continue
			}
			if emitted[result.ToolCallID] {
				duplicateResults++
				continue
			}
			emitted[result.ToolCallID] = true
			out = append(out, result)
		}
		i = end
	}
	if missing+duplicateCalls+invalidCalls+orphanResults+duplicateResults > 0 {
		log.Printf("[openai] repaired request tool pairing: missing_results=%d duplicate_calls=%d empty_call_ids=%d orphan_results=%d duplicate_results=%d", missing, duplicateCalls, invalidCalls, orphanResults, duplicateResults)
	}
	return out
}
