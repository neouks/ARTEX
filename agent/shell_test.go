package agent

import (
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestShellProfileForRunIsImmutableSnapshot(t *testing.T) {
	base := actool.ShellProfile{
		OS: "windows", Mode: "powershell", ShellPath: "powershell.exe",
		Args: []string{"-NoProfile", "-Command"}, PathStyle: "native",
	}
	run := shellProfileFor(base, `C:\work\task`)
	if run.WorkingDir != `C:\work\task` {
		t.Fatalf("working dir = %q", run.WorkingDir)
	}
	run.Args[0] = "changed"
	if base.Args[0] != "-NoProfile" {
		t.Fatalf("base profile was mutated: %#v", base.Args)
	}
}

func TestWorkerBashPromptUsesRunProfile(t *testing.T) {
	profile := actool.ShellProfile{OS: "windows", Mode: "powershell", WorkingDir: `C:\work\task`}
	for _, tool := range workerLocalTools(profile) {
		if tool.Name() != "Bash" {
			continue
		}
		prompt := tool.Prompt()
		if !strings.Contains(prompt, "PowerShell pipelines") || !strings.Contains(prompt, `C:\work\task`) {
			t.Fatalf("Bash prompt does not match run profile: %s", prompt)
		}
		if strings.Contains(prompt, "heredocs are supported") {
			t.Fatalf("PowerShell prompt advertises Bash heredocs: %s", prompt)
		}
		return
	}
	t.Fatal("worker tools do not contain Bash")
}
