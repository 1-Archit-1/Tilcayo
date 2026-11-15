package orchestrator

import (
	"fmt"
	"strings"
)

// ErrorSentinel is the prefix printed on the final stderr line by a failing worker.
const ErrorSentinel = "tilcayo-error: "

// Exit codes the orchestrator classifies, per ADR 0001.
const (
	exitWorkerError = 1   // worker called fail()
	exitKilled      = 137 // SIGKILL: out of memory or timeout, told apart by OOMKilled
	exitTerminated  = 143 // SIGTERM: cancel
)

// unescapeSentinel converts escaped characters (\n, \r, \0, \\) back into raw characters.
// This mirrors fail() in workers/shared/storage.py, which escapes exactly these four.
func unescapeSentinel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	inEscape := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inEscape {
			switch ch {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case '0':
				b.WriteByte(0)
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte('\\')
				b.WriteByte(ch)
			}
			inEscape = false
		} else if ch == '\\' {
			inEscape = true
		} else {
			b.WriteByte(ch)
		}
	}
	if inEscape {
		b.WriteByte('\\')
	}
	return b.String()
}

// lastLine returns the last non-blank line of tail with any trailing \r removed.
// It returns "" if the tail has no non-blank line.
func lastLine(tail string) string {
	var last string
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			last = line
		}
	}
	return last
}

// Classify determines the job State and error message from orchestrator signals per ADR 0001.
// logRaw is true for "worker failed without a message" and "worker crashed (exit N)":
// ADR 0001 says the caller then logs the raw tail to the control-plane log.
func Classify(exitCode int, oomKilled bool, logTail string) (state State, msg string, logRaw bool) {
	switch {
	case exitCode == 0:
		return StateSucceeded, "", false
	case oomKilled:
		return StateFailed, "out of memory", false
	case exitCode == exitKilled:
		return StateFailed, "timed out", false
	case exitCode == exitTerminated:
		return StateCancelled, "", false
	case exitCode == exitWorkerError:
		if msg, ok := strings.CutPrefix(lastLine(logTail), ErrorSentinel); ok {
			return StateFailed, unescapeSentinel(msg), false
		}
		return StateFailed, "worker failed without a message", true
	default:
		return StateFailed, fmt.Sprintf("worker crashed (exit %d)", exitCode), true
	}
}
