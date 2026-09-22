package server

import (
	"context"
	"fmt"
	actool "github.com/Autumn-27/norma/tool"
	"github.com/google/uuid"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

func shellDetectionCommand(executable, directory string, profile actool.ShellProfile) (string, error) {
	executable = strings.TrimSpace(executable)
	if executable == "" || strings.ContainsAny(executable, "\x00\r\n") || (!filepath.IsAbs(executable) && strings.ContainsAny(executable, " /\\\t")) {
		return "", fmt.Errorf("请填写命令名或绝对路径，不含参数")
	}
	quote := func(v string) string { return shellQuoteFor(profile, v) }
	candidate := executable
	if !filepath.IsAbs(candidate) && strings.TrimSpace(directory) != "" {
		candidate = filepath.Join(directory, candidate)
	}
	switch profile.Mode {
	case "cmd":
		return "", fmt.Errorf("可用性检测需 Bash 或 PowerShell 执行环境")
	case "powershell", "pwsh":
		check := ""
		if filepath.IsAbs(executable) || strings.TrimSpace(directory) != "" {
			check = "if (Test-Path -LiteralPath " + quote(candidate) + " -PathType Leaf) { (Resolve-Path -LiteralPath " + quote(candidate) + ").Path; exit 0 }; "
		}
		if filepath.IsAbs(executable) {
			return check + "throw '可执行文件不存在'", nil
		}
		return check + "$c=Get-Command -Name " + quote(executable) + " -CommandType Application -ErrorAction SilentlyContinue; if ($c) { $c.Source } else { throw '未找到可执行命令' }", nil
	default:
		check := ""
		if filepath.IsAbs(executable) || strings.TrimSpace(directory) != "" {
			check = "if [ -f " + quote(candidate) + " ] && [ -x " + quote(candidate) + " ]; then printf '%s\\n' " + quote(candidate) + "; exit 0; fi; "
		}
		if filepath.IsAbs(executable) {
			return check + "printf '%s\\n' '可执行文件不存在或无执行权限' >&2; exit 1", nil
		}
		return check + "type -P -- " + quote(executable) + " || { printf '%s\\n' '未找到可执行命令' >&2; exit 1; }", nil
	}

}
func (s *Server) testShellTool(w http.ResponseWriter, r *http.Request, req testToolReq) {
	profile := s.executionProfile()
	command := req.Command
	switch req.Action {
	case "check":
		executable := req.Executable
		if strings.TrimSpace(executable) == "" {
			executable = req.Key
		}
		var err error
		directory := strings.TrimSpace(req.Directory)
		if directory != "" && !filepath.IsAbs(directory) {
			directory = filepath.Join(s.m.dir, directory)
		}
		command, err = shellDetectionCommand(executable, directory, profile)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	case "run":
		if strings.TrimSpace(command) == "" {
			writeErr(w, 400, "请填写测试命令")
			return
		}
	default:
		writeErr(w, 400, "shell action 需为 check 或 run")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		writeJSON(w, 200, map[string]any{"output": err.Error(), "is_error": true, "duration_ms": 0})
		return
	}
	started := time.Now()
	manager, err := actool.NewManagerWithProfile("tool-test-"+uuid.NewString(), profile)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	defer manager.Cleanup()
	task, finished, output, err := manager.RunForeground(ctx, actool.SpawnSpec{Command: command, Kind: actool.KindBash, WorkingDir: s.m.dir, ShellProfile: profile}, time.Minute)
	failed := err != nil || !finished
	if task != nil {
		info, _ := manager.Get(task.ID)
		failed = failed || info.ExitCode != 0
		if err != nil {
			output, _ = manager.Output(task.ID, 0)
		}
		if finished && info.ExitCode != 0 {
			output += fmt.Sprintf("\n[exit code %d]", info.ExitCode)
		}
	}
	if err != nil {
		output += "\n" + err.Error()
	}
	output = actool.Capture(nil, output)

	writeJSON(w, 200, map[string]any{"output": output, "is_error": failed, "duration_ms": time.Since(started).Milliseconds()})
}
