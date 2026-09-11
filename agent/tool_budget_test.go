package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolDetailUnicodeAndCollections(t *testing.T) {
	text := strings.Repeat("中文🙂\n", 9000)
	var joined strings.Builder
	for offset := 0; ; {
		out, err := projectDetail(map[string]any{"body": text}, detailWindow{offset, 7999}, "/body", 0)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(out)
		if err != nil || !json.Valid(encoded) {
			t.Fatal("invalid JSON")
		}
		joined.WriteString(out["value"].(string))
		next, more := out["next_offset"].(int)
		if !more {
			break
		}
		if next <= offset {
			t.Fatal("non-progressing cursor")
		}
		offset = next
	}
	if joined.String() != text {
		t.Fatal("text lost or corrupted")
	}
	values := make([]any, 75)
	for i := range values {
		values[i] = strings.Repeat("证据", 100)
	}
	count := 0
	for index := 0; ; {
		out, err := projectDetail(values, detailWindow{0, 800}, "", index)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out["entries"].([]map[string]any) {
			if row["deferred"] == true {
				detail, err := projectDetail(values, detailWindow{0, 8000}, row["field"].(string), 0)
				if err != nil || detail["value"] != values[count] {
					t.Fatal("deferred evidence unreadable")
				}
			}
			count++
		}
		next, more := out["next_index"].(int)
		if !more {
			break
		}
		if next <= index {
			t.Fatal("non-progressing array")
		}
		index = next
	}
	if count != 75 {
		t.Fatalf("array lost entries: %d", count)
	}
}

func TestToolInputRejectsInvalidShapes(t *testing.T) {
	for _, input := range []string{`[]`, `null`, `{"limit":"20"}`, `{"unknown":1}`, `{} {}`} {
		var args struct {
			Limit int `json:"limit"`
		}
		if decodeToolInput(json.RawMessage(input), &args) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, window := range []detailWindow{{-1, 20}, {0, -1}, {0, 24001}} {
		if window.validate() == nil {
			t.Fatal("invalid window accepted")
		}
	}
}
