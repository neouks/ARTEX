package tool

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ValidateInput checks a raw JSON input against a minimal subset of JSON Schema
// sufficient for tool inputs: object type, required properties, and per-property
// primitive type checks (string/number/integer/boolean/array/object). It never
// panics; a non-nil error means the input is invalid (FR-04.5, NFR-07).
func ValidateInput(schema map[string]any, raw json.RawMessage) error {
	if schema == nil {
		return nil
	}
	var v any
	if len(raw) == 0 {
		v = map[string]any{}
	} else if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("input is not valid JSON: %w", err)
	}
	return validateValue(schema, v, "")
}

func validateValue(schema map[string]any, v any, path string) error {
	if values, exists := schema["enum"]; exists {
		allowed := reflect.ValueOf(values)
		if allowed.Kind() == reflect.Slice {
			match := false
			for i := 0; i < allowed.Len(); i++ {
				a, _ := json.Marshal(allowed.Index(i).Interface())
				b, _ := json.Marshal(v)
				if string(a) == string(b) {
					match = true
					break
				}
			}
			if !match {
				return fmt.Errorf("%s: value is not in enum", at(path))
			}
		}
	}
	typ, _ := schema["type"].(string)
	switch typ {
	case "object", "":
		m, ok := v.(map[string]any)
		if !ok {
			if typ == "" {
				return nil
			}
			return fmt.Errorf("%s: expected object", at(path))
		}
		for _, req := range toStrings(schema["required"]) {
			if _, present := m[req]; !present {
				return fmt.Errorf("%s: missing required property %q", at(path), req)
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for key, val := range m {
			ps, ok := props[key].(map[string]any)
			if !ok {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s: unknown property %q", at(path), key)
				}
				continue // unknown property — tolerate
			}
			if err := validateValue(ps, val, join(path, key)); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			return fmt.Errorf("%s: expected string", at(path))
		}
	case "integer":
		if f, ok := v.(float64); !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s: expected integer", at(path))
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return fmt.Errorf("%s: expected number", at(path))
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("%s: expected boolean", at(path))
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array", at(path))
		}
		if n, ok := schemaNumber(schema["minItems"]); ok && float64(len(arr)) < n {
			return fmt.Errorf("%s: requires at least %v items", at(path), n)
		}
		if n, ok := schemaNumber(schema["maxItems"]); ok && float64(len(arr)) > n {
			return fmt.Errorf("%s: allows at most %v items", at(path), n)
		}
		if items, ok := schema["items"].(map[string]any); ok {
			for i, el := range arr {
				if err := validateValue(items, el, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	}
	if typ == "integer" || typ == "number" {
		n, _ := v.(float64)
		if bound, ok := schemaNumber(schema["minimum"]); ok && n < bound {
			return fmt.Errorf("%s: must be >= %v", at(path), bound)
		}
		if bound, ok := schemaNumber(schema["maximum"]); ok && n > bound {
			return fmt.Errorf("%s: must be <= %v", at(path), bound)
		}
	}
	return nil
}

func schemaNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func toStrings(v any) []string {
	if values, ok := v.([]string); ok {
		return values
	}
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func at(path string) string {
	if path == "" {
		return "input"
	}
	return strings.TrimPrefix(path, ".")
}
