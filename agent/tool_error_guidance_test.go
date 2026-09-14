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
		{`{"limit":50}`, "缺少查询条件"},
		{`{}`, "缺少查询条件"},
		{`{"dsl":"  ","ids":[],"limit":50}`, "缺少查询条件"},
		{`{"type":"endpoint","limit":50}`, "缺少查询条件"},
		{`{"id":1,"dsl":"example.com"}`, "查询条件冲突"},
		{`{"id":1,"ids":[2]}`, "查询条件冲突"},
		{`{"ids":[1],"dsl":"example.com"}`, "查询条件冲突"},
		{`{"id":1,"type":"endpoint"}`, "请删除 type"},
	} {
		result, err := tool.Call(context.Background(), json.RawMessage(tc.input), nil)
		if err != nil || !result.IsError || !strings.Contains(result.Flatten(), tc.hint) {
			t.Fatalf("%s: %v %v", tc.input, result, err)
		}
	}
}

// Valid selectors must reach the store, rather than failing selector validation.
func TestAssetQuerySelectors(t *testing.T) {
	tool := NewToolSet(nil, "worker").listAssets()
	for _, input := range []string{`{"id":123}`, `{"ids":[123,456]}`, `{"dsl":"url=example.com","limit":50}`, `{"dsl":"url=example.com","type":"endpoint","offset":50}`} {
		result, err := tool.Call(context.Background(), json.RawMessage(input), nil)
		if err != nil || !result.IsError || result.Flatten() != "AssetStore 未初始化" {
			t.Fatalf("%s: %v %v", input, result, err)
		}
	}
}
