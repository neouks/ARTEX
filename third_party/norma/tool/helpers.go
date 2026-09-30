package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const defaultMaxOutput = 30000

func maxOut(tc *ToolContext) int {
	if tc != nil && tc.MaxOutputChars > 0 {
		return tc.MaxOutputChars
	}
	return defaultMaxOutput
}

var spillSeq atomic.Int64

// Capture bounds a tool's textual output for the model. When the output exceeds
// the limit (ToolContext.MaxOutputChars, default 30000) and the host configured
// an OutputDir, the FULL output is written to a file there and a tight head + a
// pointer to that file is returned — so the model can read slices on demand
// (grep/sed) instead of losing the overflow. Without an OutputDir it falls back
// to a head+tail truncation (the overflow is discarded). Tools that produce
// potentially large output should return Capture(tc, output) rather than the raw
// string.
func Capture(tc *ToolContext, s string) string {
	max := maxOut(tc)
	if len(s) <= max {
		return s
	}
	if tc != nil && tc.OutputDir != "" {
		if path, err := spillOutput(tc.OutputDir, s); err == nil {
			ref := path
			if tc.WorkingDir != "" {
				if rel, e := filepath.Rel(tc.WorkingDir, path); e == nil && !strings.HasPrefix(rel, "..") {
					ref = rel
				}
			}
			lines := strings.Count(s, "\n") + 1
			return prefixBytes(s, max) + fmt.Sprintf(
				"\n\n... <persisted-output>[Output too large: full %d bytes / %d lines.Full output saved to  %s </persisted-output>",
				len(s), lines, ref)
		}
	}
	return truncate(s, max)
}

var persistedOutputNotice = regexp.MustCompile(`^\n\n\.\.\. <persisted-output>\[Output too large: full [0-9]{1,20} bytes / [0-9]{1,20} lines\.Full output saved to  [^\r\n]+ </persisted-output>$`)
var truncatedOutputNotice = regexp.MustCompile(`\n\n\.\.\. \[[0-9]{1,20} characters truncated\] \.\.\.\n\n`)

// Recognize a complete Capture receipt AND its bounded preview, not a marker
// mentioned anywhere in arbitrary output. This also works across a deferred
// executor's independently constructed ToolContexts without double-spilling.
func alreadyCaptured(tc *ToolContext, s string) bool {
	max := maxOut(tc)
	if i := strings.LastIndex(s, "\n\n... <persisted-output>"); i >= 0 && i <= max && len(s)-i <= 4300 {
		if persistedOutputNotice.MatchString(s[i:]) {
			return true
		}
	}
	// Only scan small, already bounded previews; raw oversized output cannot
	// bypass Capture merely by containing a plausible notice in its tail.
	if len(s) <= max+80 {
		for _, span := range truncatedOutputNotice.FindAllStringIndex(s, -1) {
			if len(s)-(span[1]-span[0]) <= max {
				return true
			}
		}
	}
	return false
}

// CaptureOnce is the v0.4.3 global cap's idempotent entry point. A receipt may
// exceed the preview budget by its fixed notice and bounded file path.
func CaptureOnce(tc *ToolContext, s string) string {
	if alreadyCaptured(tc, s) {
		return s
	}
	return Capture(tc, s)
}

// The existing limit is bytes; keep it without splitting a UTF-8 code point.
func prefixBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

func spillOutput(dir, s string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("output-%d-%d.txt", time.Now().Unix(), spillSeq.Add(1))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// resolvePath joins a relative path against the working directory.
func resolvePath(tc *ToolContext, p string) string {
	if filepath.IsAbs(p) || tc == nil || tc.WorkingDir == "" {
		return p
	}
	return filepath.Join(tc.WorkingDir, p)
}

// truncate trims s to max characters, keeping head and tail.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	half := max / 2
	start := len(s) - half
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	head, tail := prefixBytes(s, half), s[start:]
	return head + fmt.Sprintf("\n\n... [%d characters truncated] ...\n\n", len(s)-len(head)-len(tail)) + tail
}
