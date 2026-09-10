package tool

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestPowerShellCommandPreservesSource(t *testing.T) {
	profile := ShellProfile{Mode: "powershell", ShellPath: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, Args: []string{"-NoProfile", "-NonInteractive", "-Command"}}
	command := "Set-Location -LiteralPath 'E:\\AI\\项目 space'\npython -c \"print('hello')\"\n# 中文 😀 $foo | head -n 100"
	_, args := shellCommand(profile, "", command)
	if args[len(args)-2] != "-EncodedCommand" {
		t.Fatalf("args=%v", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[len(args)-1])
	if err != nil {
		t.Fatal(err)
	}
	units := make([]uint16, len(raw)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(raw[2*i:])
	}
	source := string(utf16.Decode(units))
	if source != powerShellCompatibility+"\n"+command {
		t.Fatal("command source changed during transport")
	}
	if profile.Args[2] != "-Command" {
		t.Fatal("mutated shared profile")
	}
	for _, want := range []string{"curl.exe", "Select-Object -First", "python ./script.py", "backtick"} {
		if !strings.Contains(shellPrompt(profile), want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}

func TestShellCommandLeavesOtherShellsUnchanged(t *testing.T) {
	for _, profile := range []ShellProfile{
		{Mode: "bash", ShellPath: "bash", Args: []string{"-c"}},
		{Mode: "cmd", ShellPath: "cmd.exe", Args: []string{"/D", "/C"}},
		{Mode: "wsl", ShellPath: "wsl.exe", Args: []string{"--", "bash", "-lc"}},
	} {
		_, got := shellCommand(profile, "", "echo 'hello'")
		want := append(append([]string{}, profile.Args...), "echo 'hello'")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s args=%v", profile.Mode, got)
		}
	}
}

// Runs on Windows CI, or on hosts with PowerShell Core installed. Only contacts
// an in-process loopback server; never replays user URLs or credentials.
func TestPowerShellCompatibilityRuntime(t *testing.T) {
	for _, name := range []string{"powershell.exe", "pwsh"} {
		t.Run(name, func(t *testing.T) {
			path, err := exec.LookPath(name)
			if err != nil {
				t.Skip("PowerShell executable unavailable")
			}
			profile := ShellProfile{ShellPath: path, Args: []string{"-NoProfile", "-NonInteractive", "-Command"}}
			run := func(command string) string {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				shell, args := shellCommand(profile, "", command)
				out, err := exec.CommandContext(ctx, shell, args...).CombinedOutput()
				if err != nil {
					t.Fatalf("shell failed: %v\n%s", err, out)
				}
				return strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", "\n")
			}
			for _, command := range []string{"1..5 | head -n 2", "1..5 | head -2"} {
				if out := run(command); out != "1\n2" {
					t.Fatalf("head output=%q", out)
				}
			}
			if out := run("Write-Output '中文 \"quoted\"'\nWrite-Output 'second'"); out != "中文 \"quoted\"\nsecond" {
				t.Fatalf("quote output=%q", out)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Test") != "a|b" {
					t.Error("header changed")
				}
				w.Write([]byte("one\ntwo\nthree\n"))
			}))
			defer server.Close()
			if out := run("curl -s -m 5 -X GET '" + server.URL + "' -H 'X-Test: a|b' | head -n 2"); out != "one\ntwo" {
				t.Fatalf("curl output=%q", out)
			}
		})
	}
}
