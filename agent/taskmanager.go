package agent

import actool "github.com/Autumn-27/norma/tool"

func toolsNeedTaskManager(tools []actool.CoreTool) bool {
	for _, tool := range tools {
		if tool.Name() == "shell_open" {
			return true
		}
	}
	return false
}
