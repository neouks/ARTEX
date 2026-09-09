package tool

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestShellProfilePathForShell(t *testing.T) {
	if got := (ShellProfile{PathStyle: "wsl"}).PathForShell(`C:\work\repo`); got != "/mnt/c/work/repo" {
		t.Fatalf("wsl path = %q", got)
	}
	if got := (ShellProfile{PathStyle: "gitbash"}).PathForShell(`D:\work\repo`); got != "/d/work/repo" {
		t.Fatalf("gitbash path = %q", got)
	}
}

func TestShellProfileArgsForWSL(t *testing.T) {
	p := ShellProfile{Mode: "wsl", ShellPath: "wsl.exe", Args: []string{"--", "bash", "-lc"}}
	got := p.argsFor(`C:\repo`)
	want := []string{"--cd", "/mnt/c/repo", "--", "bash", "-lc"}
	if len(got) != len(want) {
		t.Fatalf("args = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args = %#v, want %#v", got, want)
		}
	}
}

func TestDetectShellForWindowsModes(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "bash.exe" || name == "bash" {
			return `C:\Program Files\Git\bin\bash.exe`, nil
		}
		return `C:\\Windows\\System32\\` + name, nil
	}
	for _, tc := range []struct {
		mode, want, style string
	}{
		{"powershell", "powershell", "native"},
		{"pwsh", "pwsh", "native"},
		{"gitbash", "gitbash", "gitbash"},
		{"wsl", "wsl", "wsl"},
		{"cmd", "cmd", "native"},
	} {
		p, err := DetectShellForOS("windows", tc.mode, lookup)
		if err != nil || p.Mode != tc.want || p.PathStyle != tc.style {
			t.Fatalf("mode %s: %#v, %v", tc.mode, p, err)
		}
	}
}

func TestDetectShellAutoUsesActualExecutor(t *testing.T) {
	lookup := func(name string) (string, error) {
		switch name {
		case "pwsh.exe":
			return `C:\Program Files\PowerShell\7\pwsh.exe`, nil
		case "cmd.exe":
			return `C:\Windows\System32\cmd.exe`, nil
		default:
			return "", errors.New("not found")
		}
	}
	p, err := DetectShellForOS("windows", "auto", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "pwsh" || p.ShellPath != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("auto profile = %#v", p)
	}

	p, err = DetectShellForOS("windows", "cmd", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Args, []string{"/D", "/V:OFF", "/S", "/C"}) {
		t.Fatalf("cmd args = %#v", p.Args)
	}
}

func TestGitBashRejectsWSLShim(t *testing.T) {
	lookup := func(string) (string, error) { return `C:\Windows\System32\bash.exe`, nil }
	if _, err := DetectShellForOS("windows", "gitbash", lookup); err == nil {
		t.Fatal("expected ambiguous WSL bash shim to be rejected")
	}
	if _, err := DetectShellForOS("windows", "bash", lookup); err == nil {
		t.Fatal("expected legacy WSL bash shim to be rejected in generic bash mode")
	}
}

func TestWindowsBashReportsGitBashWhenDetected(t *testing.T) {
	lookup := func(string) (string, error) { return `E:\tools\Git\bin\bash.exe`, nil }
	p, err := DetectShellForOS("windows", "bash", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if p.Mode != "gitbash" || p.PathStyle != "gitbash" {
		t.Fatalf("bash profile = %#v", p)
	}
}

func TestWindowsBashRejectsUnknownDistribution(t *testing.T) {
	lookup := func(string) (string, error) { return `C:\tools\cygwin\bin\bash.exe`, nil }
	if _, err := DetectShellForOS("windows", "bash", lookup); err == nil {
		t.Fatal("expected an unknown Windows bash distribution to be rejected")
	}
}

func TestDetectShellForUnixModes(t *testing.T) {
	lookup := func(name string) (string, error) {
		switch name {
		case "bash":
			return "/usr/bin/bash", nil
		case "pwsh":
			return "/usr/bin/pwsh", nil
		default:
			return "", errors.New("not found")
		}
	}
	for _, tc := range []struct {
		mode, want, style string
	}{
		{"auto", "bash", "posix"},
		{"bash", "bash", "posix"},
		{"pwsh", "pwsh", "posix"},
	} {
		p, err := DetectShellForOS("linux", tc.mode, lookup)
		if err != nil || p.Mode != tc.want || p.PathStyle != tc.style {
			t.Fatalf("mode %s: %#v, %v", tc.mode, p, err)
		}
	}
}

func TestDetectShellUnixAutoFallsBackToSh(t *testing.T) {
	lookup := func(name string) (string, error) {
		if name == "sh" {
			return "/bin/sh", nil
		}
		return "", errors.New("not found")
	}
	p, err := DetectShellForOS("linux", "auto", lookup)
	if err != nil || p.Mode != "sh" || p.ShellPath != "/bin/sh" {
		t.Fatalf("auto fallback = %#v, %v", p, err)
	}
}

func TestShellPromptMatchesProfile(t *testing.T) {
	powerShell := shellPrompt(ShellProfile{Mode: "powershell", WorkingDir: `C:\work\task`})
	for _, want := range []string{"PowerShell pipelines", "Get-ChildItem/Get-Content", "2>$null", `C:\work\task`, "remote ssh/nc"} {
		if !strings.Contains(powerShell, want) {
			t.Fatalf("PowerShell prompt missing %q: %s", want, powerShell)
		}
	}
	if strings.Contains(powerShell, "heredocs are supported") {
		t.Fatalf("PowerShell prompt advertises Bash heredocs: %s", powerShell)
	}

	wsl := shellPrompt(ShellProfile{Mode: "wsl", PathStyle: "wsl", WorkingDir: `E:\AI\ARTEX\tasks\12`})
	for _, want := range []string{"POSIX shell chaining", "WSL form", "/mnt/e/AI/ARTEX/tasks/12"} {
		if !strings.Contains(wsl, want) {
			t.Fatalf("WSL prompt missing %q: %s", want, wsl)
		}
	}
}

func TestInteractiveSupport(t *testing.T) {
	if (ShellProfile{OS: "windows"}).InteractiveSupported() {
		t.Fatal("Windows must not advertise PTY support before ConPTY is implemented")
	}
	if !(ShellProfile{OS: "linux"}).InteractiveSupported() {
		t.Fatal("Unix profile should advertise PTY support")
	}
}
