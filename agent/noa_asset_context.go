package agent

import (
	"context"

	"github.com/Autumn-27/norma/llm"
	"github.com/Autumn-27/norma/noaadapter"
)

// noa tags its model-view tool results, so strip only that trailing metadata
// while the normal authorization/deduplication pipeline parses structured JSON.
// Restore refs afterwards; neither raw audit messages nor noa's ledger change.
func (p assetContextProvider) filter(ctx context.Context, req llm.CompletionRequest) (llm.CompletionRequest, error) {
	type ref struct {
		m, b, c int
		suffix  string
	}
	var refs []ref
	for mi, m := range req.Messages {
		for bi, b := range m.Content {
			if b.Type != llm.BlockToolResult {
				continue
			}
			for ci, c := range b.Content {
				if c.Type != llm.BlockText {
					continue
				}
				plain := noaadapter.StripRefTag(c.Text)
				if plain != c.Text {
					refs = append(refs, ref{mi, bi, ci, c.Text[len(plain):]})
				}
			}
		}
	}
	if len(refs) > 0 {
		req = cloneToolMessages(req)
		for _, r := range refs {
			b := &req.Messages[r.m].Content[r.b]
			b.Content = append([]llm.ContentBlock(nil), b.Content...)
			b.Content[r.c].Text = noaadapter.StripRefTag(b.Content[r.c].Text)
		}
	}
	out, err := p.filterPlain(ctx, req)
	if err != nil {
		return out, err
	}
	for _, r := range refs {
		b := &out.Messages[r.m].Content[r.b]
		if r.c < len(b.Content) && b.Content[r.c].Type == llm.BlockText {
			b.Content[r.c].Text += r.suffix
		}
	}
	return out, nil
}
