package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestToolDetailInputValidation(t *testing.T) {
	for _, raw := range []string{``, `null`, `[]`, `{} {}`, `{"unknown":1}`, `{"field":null}`, `{"offset":"1"}`, `{"max_chars":0}`, `{"max_chars":24001}`, `{"max_chars":-1}`, `{"offset":-1}`, `{"index":-1}`, `{"field":"bad"}`, `{"index":1.5}`} {
		t.Run(raw, func(t *testing.T) {
			if ValidateToolDetailInput(json.RawMessage(raw)) == nil {
				t.Fatal("invalid request accepted")
			}
		})
	}
	for _, raw := range []string{`{}`, `{"field":"/a~1b","max_chars":1}`, `{"index":20,"offset":0,"max_chars":24000}`} {
		if err := ValidateToolDetailInput(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestToolDetailLosslessTextBudget(t *testing.T) {
	text := strings.Repeat("中文😀\"\n\t\x01<&>", 10000)
	source := map[string]any{"evidence": text}
	var rebuilt strings.Builder
	for offset := 0; ; {
		out, err := ProjectToolDetail(source, json.RawMessage(fmt.Sprintf(`{"field":"/evidence","offset":%d,"max_chars":24000}`, offset)))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(out)
		if err != nil || !utf8.Valid(raw) || len([]rune(string(raw))) > 24000 {
			t.Fatal("response exceeds budget or invalid encoding", err)
		}
		rebuilt.WriteString(out["value"].(string))
		if out["truncated"] == false {
			break
		}
		next := out["next_offset"].(int)
		if next <= offset {
			t.Fatal("non-progressing cursor")
		}
		offset = next
	}
	if rebuilt.String() != text {
		t.Fatal("evidence lost or changed")
	}
}

func TestToolDetailCollectionsAndDeferredFields(t *testing.T) {
	assets := make([]any, 65)
	for i := range assets {
		assets[i] = map[string]any{"id": i, "extra": strings.Repeat("证据", 5000)}
	}
	source := map[string]any{"assets": assets, "a/b~": true}
	root, err := ProjectToolDetail(source, json.RawMessage(`{}`))
	if err != nil || root["truncated"] != true {
		t.Fatal("large result not deferred", err)
	}
	count := 0
	for index := 0; ; {
		out, err := ProjectToolDetail(source, json.RawMessage(fmt.Sprintf(`{"field":"/assets","index":%d,"max_chars":1000}`, index)))
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out["entries"].([]map[string]any) {
			if row["deferred"] != true {
				t.Fatal("large member unexpectedly inlined")
			}
			count++
		}
		next, ok := out["next_index"].(int)
		if !ok {
			break
		}
		if next <= index {
			t.Fatal("stalled pagination")
		}
		index = next
	}
	if count != 65 {
		t.Fatalf("members=%d", count)
	}
	small, err := ProjectToolDetail(source, json.RawMessage(`{"field":"/a~1b~0"}`))
	if err != nil || small["value"] != true {
		t.Fatal("escaped pointer failed", err)
	}
	for _, raw := range []string{`{"field":"/missing"}`, `{"field":"/assets/99"}`, `{"field":"/assets/no"}`, `{"field":"/a~1b~0/x"}`, `{"field":"/a~1b~0","index":1}`, `{"offset":1}`} {
		if _, err := ProjectToolDetail(source, json.RawMessage(raw)); err == nil {
			t.Fatal("invalid continuation accepted", raw)
		}
	}
	if _, err := ProjectToolDetail(make(chan int), json.RawMessage(`{}`)); err == nil {
		t.Fatal("marshal failure hidden")
	}
	if _, err := ProjectToolDetail(source, json.RawMessage(`{"max_chars":-1}`)); err == nil {
		t.Fatal("bad budget accepted")
	}
	key := strings.Repeat("k", 24000)
	input, _ := json.Marshal(map[string]any{"field": "/" + key})
	if _, err := ProjectToolDetail(map[string]any{key: true}, input); err == nil {
		t.Fatal("unbounded field metadata accepted")
	}
}
