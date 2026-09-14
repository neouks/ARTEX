package agent

import (
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestFactWriteNamingContract(t *testing.T) {
	base := NewToolSet(nil, "worker").recordFact()
	for _, tool := range []actool.CoreTool{base, DecorateTool(base, "自定义事实说明", nil)} {
		if tool.Name() != "record_fact" || !strings.Contains(tool.Description(), factWriteContract) {
			t.Fatal("single and batch naming contract missing")
		}
		props := tool.InputSchema()["properties"].(map[string]any)
		if props["facts"] == nil {
			t.Fatal("batch parameter missing")
		}
		r := actool.NewRegistry(tool)
		if _, ok := r.Get("record_facts"); ok {
			t.Fatal("must not register a compatibility alias")
		}
		if len(r.Schemas()) != 1 {
			t.Fatal("duplicate model schema")
		}
	}
	if !strings.Contains(settleWrapUpPrompt, factWriteContract) {
		t.Fatal("settlement naming rule missing")
	}
	for _, seed := range BuiltinToolSeeds() {
		if seed.Key == "record_facts" {
			t.Fatal("invalid tool seed")
		}
	}
}
