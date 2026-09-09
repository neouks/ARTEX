package agent

import actool "github.com/Autumn-27/norma/tool"

func shellProfileFor(profile actool.ShellProfile, workDir string) actool.ShellProfile {
	profile.Args = append([]string(nil), profile.Args...)
	profile.WorkingDir = workDir
	return profile
}
