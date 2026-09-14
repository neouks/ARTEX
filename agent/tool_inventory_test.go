package agent

import (
	"encoding/json"
	"regexp"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestBuiltinToolInventoryContracts(t *testing.T) {
	validName := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	var check func(map[string]any, string)
	check = func(schema map[string]any, path string) {
		props, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if props[key.(string)] == nil {
					t.Errorf("%s missing required property %v", path, key)
				}
			}
		}
		for key, value := range props {
			if child, ok := value.(map[string]any); ok {
				check(child, path+"."+key)
			}
		}
		if items, ok := schema["items"].(map[string]any); ok {
			check(items, path+"[]")
		}
	}
	for _, seed := range BuiltinToolSeeds() {
		if !validName.MatchString(seed.Key) {
			t.Errorf("invalid builtin name %s", seed.Key)
		}
		raw, err := json.Marshal(seed.Schema)
		if err != nil {
			t.Fatal(err)
		}
		var normalized map[string]any
		json.Unmarshal(raw, &normalized)
		check(normalized, seed.Key)
	}
	for role, tools := range builtinToolsByAgent() {
		seen := map[string]bool{}
		for _, tool := range tools {
			if seen[tool.Name()] {
				t.Errorf("%s duplicate tool %s", role, tool.Name())
			}
			seen[tool.Name()] = true
		}
		if len(actool.NewRegistry(tools...).Schemas()) != len(tools) {
			t.Errorf("%s registry silently replaces a tool", role)
		}
	}
	t.Logf("checked %d built-in domain tool schemas", len(BuiltinToolSeeds()))
}
