package agent

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// renderLightTagged renders structured prompt context without JSON punctuation or
// repeated object delimiters. Persisted data remains unchanged; this is only the
// bounded, model-facing projection.
func renderLightTagged(root string, value any, maxRunes int) string {
	var body strings.Builder
	writeLightValue(&body, reflect.ValueOf(value), 0)
	return wrapLightTagged(root, body.String(), maxRunes)
}

// renderLightTaggedOrdered renders a string-keyed map with high-value fields
// first, then appends all remaining fields in stable lexical order. This keeps
// task identity and active work visible even when a large context is truncated.
func renderLightTaggedOrdered(root string, value map[string]any, priority []string, maxRunes int) string {
	var body strings.Builder
	written := make(map[string]bool, len(value))
	for _, key := range priority {
		item, ok := value[key]
		if !ok {
			continue
		}
		writeLightMapEntry(&body, reflect.ValueOf(key), reflect.ValueOf(item), 0)
		written[key] = true
	}
	keys := make([]string, 0, len(value)-len(written))
	for key := range value {
		if !written[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		writeLightMapEntry(&body, reflect.ValueOf(key), reflect.ValueOf(value[key]), 0)
	}
	return wrapLightTagged(root, body.String(), maxRunes)
}

func wrapLightTagged(root, body string, maxRunes int) string {
	bodyText := strings.TrimSpace(body)
	truncated := false
	if maxRunes > 0 {
		runes := []rune(bodyText)
		if len(runes) > maxRunes {
			bodyText = strings.TrimSpace(string(runes[:maxRunes]))
			truncated = true
		}
	}
	var out strings.Builder
	out.WriteString("<")
	out.WriteString(root)
	out.WriteString(">\n")
	out.WriteString(bodyText)
	if truncated {
		out.WriteString("\n<truncated>true</truncated>")
	}
	out.WriteString("\n</")
	out.WriteString(root)
	out.WriteString(">")
	return out.String()
}

func writeLightValue(out *strings.Builder, value reflect.Value, indent int) {
	value = dereference(value)
	if !value.IsValid() {
		writeIndent(out, indent)
		out.WriteString("null\n")
		return
	}
	switch value.Kind() {
	case reflect.Map:
		keys := value.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
		for _, key := range keys {
			writeLightMapEntry(out, key, value.MapIndex(key), indent)
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			item := dereference(value.Index(i))
			writeIndent(out, indent)
			out.WriteString("- ")
			if isLightScalar(item) {
				out.WriteString(lightScalar(item))
				out.WriteByte('\n')
				continue
			}
			if item.Kind() == reflect.Map && lightMapInline(item) {
				out.WriteString(lightInlineMap(item))
				out.WriteByte('\n')
				continue
			}
			out.WriteByte('\n')
			writeLightValue(out, item, indent+2)
		}
	case reflect.Struct:
		mapped := make(map[string]any)
		typ := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			mapped[name] = value.Field(i).Interface()
		}
		writeLightValue(out, reflect.ValueOf(mapped), indent)
	default:
		writeIndent(out, indent)
		out.WriteString(lightScalar(value))
		out.WriteByte('\n')
	}
}

func writeLightMapEntry(out *strings.Builder, key, value reflect.Value, indent int) {
	item := dereference(value)
	writeIndent(out, indent)
	out.WriteString(lightText(fmt.Sprint(key.Interface())))
	if isLightScalar(item) {
		out.WriteByte('=')
		out.WriteString(lightScalar(item))
		out.WriteByte('\n')
		return
	}
	out.WriteString(":\n")
	writeLightValue(out, item, indent+2)
}

func dereference(value reflect.Value) reflect.Value {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			return reflect.Value{}
		}
		value = value.Elem()
	}
	return value
}

func isLightScalar(value reflect.Value) bool {
	if !value.IsValid() {
		return true
	}
	switch value.Kind() {
	case reflect.Map, reflect.Struct:
		return false
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !isLightScalar(dereference(value.Index(i))) {
				return false
			}
		}
	}
	return true
}

func lightMapInline(value reflect.Value) bool {
	for _, key := range value.MapKeys() {
		if !isLightScalar(dereference(value.MapIndex(key))) {
			return false
		}
	}
	return true
}

func lightInlineMap(value reflect.Value) string {
	keys := value.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", lightText(fmt.Sprint(key.Interface())), lightScalar(dereference(value.MapIndex(key)))))
	}
	return strings.Join(parts, " | ")
}

func lightScalar(value reflect.Value) string {
	if !value.IsValid() {
		return "null"
	}
	if value.Kind() == reflect.Slice || value.Kind() == reflect.Array {
		parts := make([]string, 0, value.Len())
		for i := 0; i < value.Len(); i++ {
			parts = append(parts, lightScalar(dereference(value.Index(i))))
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return lightText(fmt.Sprint(value.Interface()))
}

func lightText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
	const maxFieldRunes = 800
	if runes := []rune(text); len(runes) > maxFieldRunes {
		text = string(runes[:maxFieldRunes]) + "..."
	}
	return text
}

func writeIndent(out *strings.Builder, n int) {
	out.WriteString(strings.Repeat(" ", n))
}
