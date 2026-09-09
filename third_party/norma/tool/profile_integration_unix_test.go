//go:build !windows

package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestShellProfileSharedByForegroundBackgroundAndPTY(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "profile.log")
	wrapper := filepath.Join(dir, "profile-shell")
	script := `#!/bin/sh
printf '%s\t%s\t%s\n' "$1" "$PWD" "$2" >> "$PROFILE_LOG"
exec /bin/sh -c "$2"
`
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	profile := ShellProfile{
		OS: "linux", Mode: "bash", ShellPath: wrapper,
		Args: []string{"profile-marker"}, PathStyle: "posix", WorkingDir: dir,
	}
	env := []string{"PROFILE_LOG=" + logPath}

	foreground := &ToolContext{WorkingDir: dir, Env: env, ShellProfile: profile}
	res, err := runBashSync(context.Background(), foreground, "printf foreground", 5*time.Second)
	if err != nil || res.IsError {
		t.Fatalf("foreground failed: %v, %s", err, res.Flatten())
	}

	m, err := NewManagerWithProfile("profile-integration-"+t.Name(), profile)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Cleanup)
	background, err := m.Spawn(SpawnSpec{Command: "printf background", WorkingDir: dir, Env: env})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-background.done:
	case <-time.After(5 * time.Second):
		t.Fatal("background command did not exit")
	}

	session, err := m.openSession("printf pty", dir, env, 24, 80, "hands-free", nil, profile)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.done:
	case <-time.After(5 * time.Second):
		t.Fatal("PTY command did not exit")
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("profile log = %q", raw)
	}
	wantDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i, command := range []string{"printf foreground", "printf background", "printf pty"} {
		want := strings.Join([]string{"profile-marker", wantDir, command}, "\t")
		if lines[i] != want {
			t.Fatalf("path %d profile = %q, want %q", i, lines[i], want)
		}
	}
}

func TestRunBashSyncAllowsNilToolContext(t *testing.T) {
	res, err := runBashSync(context.Background(), nil, "printf nil-context", 5*time.Second)
	if err != nil || res.IsError || !strings.Contains(res.Flatten(), "nil-context") {
		t.Fatalf("result=%q isError=%v err=%v", res.Flatten(), res.IsError, err)
	}
}
