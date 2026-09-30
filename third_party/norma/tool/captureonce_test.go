package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// First pass spills oversized output; a second pass over that result must not
// spill again or change it — the marker makes CaptureOnce idempotent, which is
// what lets a global post-tool net coexist with tools that Capture themselves.
func TestCaptureOnceSpillIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	tc := &ToolContext{WorkingDir: dir, OutputDir: dir, MaxOutputChars: 100}
	long := strings.Repeat("x", 5000)

	first := CaptureOnce(tc, long)
	if !strings.Contains(first, "<persisted-output>") {
		t.Fatalf("first CaptureOnce should spill oversized output: %q", first)
	}
	if files, _ := os.ReadDir(dir); len(files) != 1 {
		t.Fatalf("expected exactly 1 spill file, got %d", len(files))
	}

	second := CaptureOnce(tc, first)
	if second != first {
		t.Fatalf("CaptureOnce not idempotent on spilled output:\n first=%q\n second=%q", first, second)
	}
	if files, _ := os.ReadDir(dir); len(files) != 1 {
		t.Fatalf("second CaptureOnce spilled again: %d files", len(files))
	}
}

// Without an OutputDir, Capture falls back to head+tail truncation (a different
// marker). CaptureOnce must recognise that too and not re-truncate.
func TestCaptureOnceTruncationIsIdempotent(t *testing.T) {
	tc := &ToolContext{MaxOutputChars: 100}
	long := strings.Repeat("y", 5000)

	first := CaptureOnce(tc, long)
	if !strings.Contains(first, "characters truncated]") {
		t.Fatalf("expected truncation marker: %q", first)
	}
	if second := CaptureOnce(tc, first); second != first {
		t.Fatalf("CaptureOnce re-truncated already-truncated output")
	}
}

func TestCaptureOncePassesShortOutput(t *testing.T) {
	tc := &ToolContext{MaxOutputChars: 100}
	if got := CaptureOnce(tc, "ok"); got != "ok" {
		t.Fatalf("short output changed: %q", got)
	}
}

func TestCaptureOnceMarkerMentionsCannotBypassCap(t *testing.T) {
	tc := &ToolContext{MaxOutputChars: 100}
	for _, marker := range []string{"<persisted-output>", "characters truncated]", "\n\n... [999 characters truncated] ...\n\n", "\n\n... <persisted-output>[Output too large: full 99999 bytes / 1 lines.Full output saved to  x.txt </persisted-output>"} {
		for _, input := range []string{marker + strings.Repeat("x", 5000), strings.Repeat("x", 5000) + marker} {
			got := CaptureOnce(tc, input)
			if len(got) >= 200 {
				t.Fatalf("marker bypassed output budget: bytes=%d", len(got))
			}
		}
	}
	// Even a marker-shaped suffix cannot contain an unbounded fake file path.
	input := "\n\n... <persisted-output>[Output too large: full 99999 bytes / 1 lines.Full output saved to  " + strings.Repeat("x", 5000) + " </persisted-output>"
	if len(CaptureOnce(tc, input)) >= 200 {
		t.Fatal("fake path bypassed budget")
	}
}

func TestCaptureOnceUTF8AndSpillFailure(t *testing.T) {
	input := strings.Repeat("中文🙂\n", 100)
	for _, max := range []int{1, 2, 3, 7, 11, 100} {
		for _, spill := range []bool{false, true} {
			dir := ""
			if spill {
				dir = t.TempDir()
			}
			tc := &ToolContext{MaxOutputChars: max, OutputDir: dir}
			first := Capture(tc, input)
			if !utf8.ValidString(first) {
				t.Fatalf("invalid UTF8 max=%d spill=%t", max, spill)
			}
			if second := CaptureOnce(tc, first); second != first {
				t.Fatalf("re-captured UTF8 max=%d spill=%t", max, spill)
			}
			if spill {
				files, err := os.ReadDir(dir)
				if err != nil || len(files) != 1 {
					t.Fatalf("spill files=%d err=%v", len(files), err)
				}
				raw, err := os.ReadFile(filepath.Join(dir, files[0].Name()))
				if err != nil || string(raw) != input {
					t.Fatal("full content was not preserved", err)
				}
			}
		}
	}
	badPath := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(badPath, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	tc := &ToolContext{OutputDir: badPath, MaxOutputChars: 11}
	first := CaptureOnce(tc, input)
	if strings.Contains(first, "<persisted-output>") || !utf8.ValidString(first) || len(first) > 100 {
		t.Fatal("bad fallback", first)
	}
	if CaptureOnce(tc, first) != first {
		t.Fatal("fallback not idempotent")
	}
}
