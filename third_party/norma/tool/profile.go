package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ShellProfile is the immutable execution environment captured when an agent
// run starts. Foreground, background and interactive commands all consume it.
type ShellProfile struct {
	OS         string
	Mode       string
	ShellPath  string
	Args       []string
	PathStyle  string // native, posix, gitbash, wsl
	WorkingDir string
}

type ExecutionProfile = ShellProfile

func (p ShellProfile) valid() bool { return strings.TrimSpace(p.ShellPath) != "" }

func shellProfile(tc *ToolContext) ShellProfile {
	if tc == nil {
		return ShellProfile{}
	}
	return tc.ShellProfile
}

// LookPath is replaceable in tests so Windows detection is deterministic.
var LookPath = exec.LookPath

// DetectShellWithLookup is the injectable form used by cross-platform tests
// and embedders that provide their own executable discovery.
func DetectShellWithLookup(mode string, lookup func(string) (string, error)) (ShellProfile, error) {
	if lookup == nil {
		return DetectShell(mode)
	}
	return detectShell(runtime.GOOS, mode, lookup)
}

func DetectShellForOS(osName, mode string, lookup func(string) (string, error)) (ShellProfile, error) {
	if lookup == nil {
		lookup = exec.LookPath
	}
	return detectShell(osName, mode, lookup)
}

// DetectShell resolves a requested mode. auto reports the shell norma will
// actually execute and never claims Git Bash/WSL unless explicitly selected.
func DetectShell(mode string) (ShellProfile, error) {
	return detectShell(runtime.GOOS, mode, LookPath)
}

func detectShell(osName, mode string, lookPath func(string) (string, error)) (ShellProfile, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	allowed := map[string]bool{"auto": true, "powershell": true, "pwsh": true, "gitbash": true, "wsl": true, "bash": true, "cmd": true}
	if !allowed[mode] {
		return ShellProfile{}, fmt.Errorf("shell_mode must be one of auto, powershell, pwsh, gitbash, wsl, bash, cmd")
	}
	if osName != "windows" {
		if mode == "powershell" || mode == "gitbash" || mode == "wsl" || mode == "cmd" {
			return ShellProfile{}, fmt.Errorf("shell mode %q is not available on %s", mode, osName)
		}
		if mode == "pwsh" {
			path, err := lookPath("pwsh")
			if err != nil {
				return ShellProfile{}, fmt.Errorf("pwsh not found: %w", err)
			}
			return ShellProfile{OS: osName, Mode: "pwsh", ShellPath: path, Args: []string{"-NoProfile", "-NonInteractive", "-Command"}, PathStyle: "posix"}, nil
		}
		path, err := lookPath("bash")
		if err == nil {
			return ShellProfile{OS: osName, Mode: "bash", ShellPath: path, Args: []string{"-c"}, PathStyle: "posix"}, nil
		}
		if mode == "bash" {
			return ShellProfile{}, fmt.Errorf("bash not found: %w", err)
		}
		if path, err := lookPath("sh"); err == nil {
			return ShellProfile{OS: osName, Mode: "sh", ShellPath: path, Args: []string{"-c"}, PathStyle: "posix"}, nil
		}
		return ShellProfile{}, fmt.Errorf("no usable POSIX shell found")
	}
	lookup := func(names ...string) (string, error) {
		var last error
		for _, name := range names {
			if p, err := lookPath(name); err == nil {
				return p, nil
			} else {
				last = err
			}
		}
		return "", last
	}
	newPS := func(path, m string) ShellProfile {
		return ShellProfile{OS: "windows", Mode: m, ShellPath: path, Args: []string{"-NoProfile", "-NonInteractive", "-Command"}, PathStyle: "native"}
	}
	switch mode {
	case "auto":
		if path, err := lookup("powershell.exe", "powershell"); err == nil {
			return newPS(path, "powershell"), nil
		}
		if path, err := lookup("pwsh.exe", "pwsh"); err == nil {
			return newPS(path, "pwsh"), nil
		}
		if path, err := lookup("cmd.exe", "cmd"); err == nil {
			return ShellProfile{OS: "windows", Mode: "cmd", ShellPath: path, Args: []string{"/D", "/V:OFF", "/S", "/C"}, PathStyle: "native"}, nil
		}
		return ShellProfile{}, fmt.Errorf("powershell, pwsh and cmd not found")
	case "powershell", "pwsh":
		path, err := lookup(map[string]string{"powershell": "powershell.exe", "pwsh": "pwsh.exe"}[mode], mode)
		if err != nil {
			return ShellProfile{}, fmt.Errorf("%s not found: %w", mode, err)
		}
		return newPS(path, mode), nil
	case "gitbash":
		candidates := []string{"bash.exe", "bash"}
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			candidates = append(candidates,
				filepath.Join(root, "Git", "bin", "bash.exe"),
				filepath.Join(root, "Programs", "Git", "bin", "bash.exe"),
			)
		}
		var ambiguous string
		for _, candidate := range candidates {
			path, err := lookPath(candidate)
			if err != nil {
				continue
			}
			if isGitBashPath(path) {
				return ShellProfile{OS: "windows", Mode: "gitbash", ShellPath: path, Args: []string{"-lc"}, PathStyle: "gitbash"}, nil
			}
			if ambiguous == "" {
				ambiguous = path
			}
		}
		if ambiguous != "" {
			return ShellProfile{}, fmt.Errorf("bash resolved to %q but is not identifiable as Git Bash", ambiguous)
		}
		return ShellProfile{}, fmt.Errorf("Git Bash bash.exe not found")
	case "bash":
		path, err := lookup("bash.exe", "bash")
		if err != nil {
			return ShellProfile{}, fmt.Errorf("bash.exe not found: %w", err)
		}
		if isGitBashPath(path) {
			return ShellProfile{OS: "windows", Mode: "gitbash", ShellPath: path, Args: []string{"-lc"}, PathStyle: "gitbash"}, nil
		}
		if isLegacyWSLBashPath(path) {
			return ShellProfile{}, fmt.Errorf("bash resolved to the legacy WSL shim %q; select wsl mode", path)
		}
		return ShellProfile{}, fmt.Errorf("bash resolved to %q but its Windows path style is unknown; select gitbash or wsl mode", path)
	case "wsl":
		path, err := lookup("wsl.exe", "wsl")
		if err != nil {
			return ShellProfile{}, fmt.Errorf("wsl.exe not found: %w", err)
		}
		return ShellProfile{OS: "windows", Mode: "wsl", ShellPath: path, Args: []string{"--", "bash", "-lc"}, PathStyle: "wsl"}, nil
	case "cmd":
		path, err := lookup("cmd.exe", "cmd")
		if err != nil {
			return ShellProfile{}, fmt.Errorf("cmd.exe not found: %w", err)
		}
		return ShellProfile{OS: "windows", Mode: "cmd", ShellPath: path, Args: []string{"/D", "/V:OFF", "/S", "/C"}, PathStyle: "native"}, nil
	}
	return ShellProfile{}, fmt.Errorf("unsupported shell mode %q", mode)
}

func isGitBashPath(path string) bool {
	path = strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	return strings.Contains(path, "/git/") && strings.HasSuffix(path, "/bash.exe")
}

func isLegacyWSLBashPath(path string) bool {
	path = strings.ToLower(strings.ReplaceAll(path, `\`, "/"))
	return strings.HasSuffix(path, "/windows/system32/bash.exe")
}

func (p ShellProfile) argsFor(workDir string) []string {
	if strings.TrimSpace(workDir) == "" {
		workDir = p.WorkingDir
	}
	args := append([]string(nil), p.Args...)
	if p.Mode == "wsl" && strings.TrimSpace(workDir) != "" {
		args = append([]string{"--cd", toWSLPath(workDir)}, args...)
	}
	return args
}

func toWSLPath(path string) string {
	p := filepath.Clean(path)
	if len(p) >= 2 && p[1] == ':' {
		return "/mnt/" + strings.ToLower(string(p[0])) + strings.ReplaceAll(p[2:], `\`, "/")
	}
	return filepath.ToSlash(p)
}

// PathForShell converts a native path for shell interpolation where needed.
func (p ShellProfile) PathForShell(path string) string {
	if p.PathStyle == "wsl" {
		return toWSLPath(path)
	}
	if p.PathStyle == "gitbash" {
		v := filepath.Clean(path)
		if len(v) >= 2 && v[1] == ':' {
			return "/" + strings.ToLower(string(v[0])) + strings.ReplaceAll(v[2:], `\`, "/")
		}
		return filepath.ToSlash(v)
	}
	return path
}

// InteractiveSupported reports whether norma can attach the configured shell
// to a native pseudo-terminal on this platform. Windows execution is supported
// for foreground/background commands, but ConPTY is not implemented yet.
func (p ShellProfile) InteractiveSupported() bool {
	return p.OS != "windows"
}
