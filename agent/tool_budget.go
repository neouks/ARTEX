package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

const toolListBudget = 24000

func textWindow(text string, w detailWindow) (string, int, int) {
	r := []rune(text)
	start := min(w.Offset, len(r))
	end := start + min(w.MaxChars, len(r)-start)
	return string(r[start:end]), len(r), end
}

func decodeToolInput(raw json.RawMessage, v any) error {
	if b := bytes.TrimSpace(raw); len(b) == 0 || b[0] != '{' {
		return fmt.Errorf("参数必须是 JSON 对象")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for key, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("参数 %s 不允许为 null", key)
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("参数包含多余内容")
	}
	return nil
}

type detailWindow struct {
	Offset   int `json:"offset"`
	MaxChars int `json:"max_chars"`
}

func (w *detailWindow) validate() error {
	if w.Offset < 0 {
		return fmt.Errorf("offset 不能为负数")
	}
	if w.MaxChars == 0 {
		w.MaxChars = 8000
	}
	if w.MaxChars < 1 || w.MaxChars > 24000 {
		return fmt.Errorf("max_chars 必须为 1..24000")
	}
	return nil
}

// Structured projection: large children are explicitly deferred, not silently
// dropped. Their JSON-pointer field can be read independently, with string and
// collection continuations. No serialization fragment is presented as JSON.
func projectDetail(value any, w detailWindow, field string, index int) (map[string]any, error) {
	if index < 0 {
		return nil, fmt.Errorf("index 不能为负数")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if field != "" {
		if !strings.HasPrefix(field, "/") {
			return nil, fmt.Errorf("field 必须为 JSON Pointer")
		}
		for _, part := range strings.Split(field[1:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			switch x := v.(type) {
			case map[string]any:
				var ok bool
				v, ok = x[part]
				if !ok {
					return nil, fmt.Errorf("field 不存在")
				}
			case []any:
				i, err := strconv.Atoi(part)
				if err != nil || i < 0 || i >= len(x) {
					return nil, fmt.Errorf("field 数组索引无效")
				}
				v = x[i]
			default:
				return nil, fmt.Errorf("field 不是可展开对象")
			}
		}
	}
	out := map[string]any{"field": field}
	if text, ok := v.(string); ok {
		if index != 0 {
			return nil, fmt.Errorf("文本不支持 index")
		}
		part, total, next := textWindow(text, w)
		out["value"] = part
		out["offset"] = w.Offset
		out["total_chars"] = total
		out["truncated"] = next < total
		if next < total {
			out["next_offset"] = next
		}
		return out, nil
	}
	if w.Offset != 0 {
		return nil, fmt.Errorf("offset 仅用于文本字段")
	}
	type entry struct {
		path  string
		value any
	}
	var entries []entry
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			entries = append(entries, entry{field + "/" + escapePointer(k), x[k]})
		}
	case []any:
		for i, val := range x {
			entries = append(entries, entry{field + "/" + strconv.Itoa(i), val})
		}
	default:
		if index != 0 {
			return nil, fmt.Errorf("标量不支持 index")
		}
		out["value"] = v
		out["truncated"] = false
		return out, nil
	}
	encoded, _ := json.Marshal(v)
	if index == 0 && len([]rune(string(encoded))) <= w.MaxChars {
		out["value"] = v
		out["truncated"] = false
		return out, nil
	}
	rows := []map[string]any{}
	used := 0
	next := min(index, len(entries))
	deferred := false
	for next < len(entries) && len(rows) < 20 {
		e := entries[next]
		b, _ := json.Marshal(e.value)
		row := map[string]any{"field": e.path, "value": e.value}
		if len([]rune(string(b))) > max(256, w.MaxChars-used-128) {
			row = map[string]any{"field": e.path, "deferred": true, "chars": len([]rune(string(b)))}
			deferred = true
		}
		b, _ = json.Marshal(row)
		size := len([]rune(string(b)))
		if len(rows) > 0 && used+size > w.MaxChars {
			break
		}
		if size > toolListBudget-512 {
			return nil, fmt.Errorf("字段路径超出工具响应预算")
		}
		rows = append(rows, row)
		used += size
		next++
	}
	out["entries"] = rows
	out["index"] = index
	out["total_entries"] = len(entries)
	out["truncated"] = next < len(entries) || deferred
	if next < len(entries) {
		out["next_index"] = next
	}
	out["read_hint"] = "用条目的 field 读取延期字段；next_index 用于当前集合续页，next_offset 用于文本续读"
	return out, nil
}
func escapePointer(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
func budgetRows(rows []map[string]any) ([]map[string]any, bool) {
	size := 256
	for i, row := range rows {
		b, _ := json.Marshal(row)
		size += len([]rune(string(b))) + 1
		if size > toolListBudget {
			return rows[:i], true
		}
	}
	return rows, false
}
