package harness

import (
	"slices"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/tool"
)

// capOutput preserves errors, non-text blocks and extra messages. Clone the
// result slice so the request guard cannot mutate a tool's shared source data.
func capOutput(tc *tool.ToolContext, res tool.Result) tool.Result {
	res.Content = slices.Clone(res.Content)
	for i, b := range res.Content {
		if b.Type == llm.BlockText {
			res.Content[i].Text = tool.CaptureOnce(tc, b.Text)
		}
	}
	return res
}
