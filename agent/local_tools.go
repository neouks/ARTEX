package agent

import actool "github.com/Autumn-27/norma/tool"

// workerLocalTools is DefaultTools without Sleep. Background work is disabled for
// focused workers, so exposing a polling-only wait tool costs schema tokens without
// adding an execution path the worker should use.
func workerLocalTools() []actool.CoreTool {
	return []actool.CoreTool{
		actool.NewRead(), actool.NewWrite(), actool.NewEdit(), actool.NewMultiEdit(),
		actool.NewLS(), actool.NewGlob(), actool.NewGrep(), actool.NewBash(),
	}
}
