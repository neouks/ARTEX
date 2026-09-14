package harness

import (
	"sort"
	"strings"
	"unicode"

	"github.com/Autumn-27/norma/llm"
)

// Suggestions are diagnostics only. Never dispatch an unknown name, modify its
// arguments, or expose tools absent from the current model-visible schema.
func toolNameHint(name string, schemas []llm.ToolSchema) string {
	normalize := func(s string) string {
		return strings.Map(func(r rune) rune {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				return unicode.ToLower(r)
			}
			return -1
		}, s)
	}
	want := normalize(name)
	if len(want) > 128 {
		return ""
	}
	type candidate struct {
		name     string
		distance int
	}
	var matches []candidate
	for _, schema := range schemas {
		other := normalize(schema.Name)
		if len(other) > 128 {
			continue
		}
		prev := make([]int, len(other)+1)
		for j := range prev {
			prev[j] = j
		}
		for i := 0; i < len(want); i++ {
			next := make([]int, len(other)+1)
			next[0] = i + 1
			for j := 0; j < len(other); j++ {
				cost := 0
				if want[i] != other[j] {
					cost = 1
				}
				next[j+1] = min(next[j]+1, prev[j+1]+1, prev[j]+cost)
			}
			prev = next
		}
		if distance := prev[len(other)]; distance <= 3 {
			matches = append(matches, candidate{schema.Name, distance})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].distance != matches[j].distance {
			return matches[i].distance < matches[j].distance
		}
		return matches[i].name < matches[j].name
	})
	var names []string
	for _, match := range matches[:min(3, len(matches))] {
		names = append(names, match.name)
	}
	if len(names) == 0 {
		return ""
	}
	return " 可用的相近工具名：" + strings.Join(names, ", ") + "。请核对本轮工具定义并修正名称及参数后重试；本次未执行或写入任何内容。"
}
