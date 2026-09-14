package tool

import (
	"encoding/json"
	"testing"
)

func TestSchemaContractsBeforeAndAfterJSONRoundTrip(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"limit", "ids", "status"}, "properties": map[string]any{
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
		"ids":    map[string]any{"type": "array", "minItems": 1, "maxItems": 2, "items": map[string]any{"type": "integer"}},
		"status": map[string]any{"type": "string", "enum": []string{"pending", "approved"}},
	}}
	raw, _ := json.Marshal(schema)
	var roundtrip map[string]any
	json.Unmarshal(raw, &roundtrip)
	for _, current := range []map[string]any{schema, roundtrip} {
		for _, input := range []string{`{}`, `{"limit":60,"ids":[1],"status":"pending"}`, `{"limit":1,"ids":[],"status":"pending"}`, `{"limit":1,"ids":[1,2,3],"status":"pending"}`, `{"limit":1,"ids":[1],"status":"unknown"}`, `{"limit":1,"ids":[1],"status":"pending","url":"x"}`} {
			if err := ValidateInput(current, json.RawMessage(input)); err == nil {
				t.Fatalf("invalid input accepted: %s", input)
			}
		}
		if err := ValidateInput(current, json.RawMessage(`{"limit":50,"ids":[1,2],"status":"approved"}`)); err != nil {
			t.Fatal(err)
		}
	}
}
