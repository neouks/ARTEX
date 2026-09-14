package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAssetQueryErrorGuidance(t *testing.T) {
	tool := NewToolSet(nil, "worker").listAssets()
	for _, tc := range []struct{ input, hint string }{
		{`{"type":"endpoint","dsl":"example.com","limit":60}`, "最大50"},
		{`{"type":"endpoint","url":"example.com","limit":60}`, "没有顶层 url/domain/ip"},
		{`{"dsl":"example.com","offset":-1}`, "offset 必须"},
	} {
		result, err := tool.Call(context.Background(), json.RawMessage(tc.input), nil)
		if err != nil || !result.IsError || !strings.Contains(result.Flatten(), tc.hint) {
			t.Fatalf("%s: %v %v", tc.input, result, err)
		}
	}
}
