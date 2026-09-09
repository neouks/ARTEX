//go:build windows

package server

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func TestCustomCommandQuotingWindows(t *testing.T) {
	if os.Getenv("ARTEX_QUOTE_HELPER") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				_ = json.NewEncoder(os.Stdout).Encode(os.Args[i+1:])
				os.Exit(0)
			}
		}
		os.Exit(2)
	}

	values := []string{
		"plain",
		"with space",
		`double"quote`,
		"single'quote",
		`trailing\`,
		"& | < > ^ %PATH% !bang!",
		"line1\nline2",
	}
	profiles := []actool.ShellProfile{
		{OS: "windows", Mode: "powershell", ShellPath: "powershell.exe", Args: []string{"-NoProfile", "-NonInteractive", "-Command"}},
		{OS: "windows", Mode: "cmd", ShellPath: "cmd.exe", Args: []string{"/D", "/V:OFF", "/S", "/C"}},
	}
	for _, profile := range profiles {
		t.Run(profile.Mode, func(t *testing.T) {
			for _, value := range values {
				t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
					program := shellQuoteFor(profile, os.Args[0])
					if profile.Mode == "powershell" {
						program = "& " + program
					}
					tmpl := program + " -test.run=TestCustomCommandQuotingWindows -- {value}"
					command, paramEnv := renderCommandTemplate(tmpl, map[string]any{"value": value}, profile)
					if profile.Mode == "cmd" {
						profile.Args = cmdDelayedExpansionArgs(profile.Args)
					}
					cmd := exec.Command(profile.ShellPath, append(profile.Args, command)...)
					cmd.Env = append(os.Environ(), "ARTEX_QUOTE_HELPER=1")
					cmd.Env = append(cmd.Env, paramEnv...)
					out, err := cmd.CombinedOutput()
					if err != nil {
						t.Fatalf("command failed: %v\n%s\ncommand: %s", err, out, command)
					}
					var got []string
					if err := json.Unmarshal(out, &got); err != nil {
						t.Fatalf("decode helper output %q: %v", out, err)
					}
					if want := []string{value}; !reflect.DeepEqual(got, want) {
						t.Fatalf("round trip = %#v, want %#v; command=%s", got, want, command)
					}
				})
			}
		})
	}
}
