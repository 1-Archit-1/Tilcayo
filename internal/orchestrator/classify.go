package orchestrator

import (
	"fmt"
	"strings"
)

// ErrorSentinel is the prefix printed on the final stderr line by a failing worker.
const ErrorSentinel = "tilcayo-error: "

// UnescapeSentinel converts escaped characters (\n, \r, \\) back into raw characters.
func UnescapeSentinel(s string) string {
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

// Classify determines the job State and error message from orchestrator signals per ADR 0001.
func Classify(exitCode int, oomKilled bool, logTail string) (State, string) {
	if exitCode == 0 {
		return StateSucceeded, ""
	} else if exitCode == 137 && oomKilled {
		return StateFailed, "out of memory"
	} else if exitCode == 137 && !oomKilled {
		return StateFailed, "timed out"
	} else if exitCode == 143 {
		return StateCancelled, ""
	} else if exitCode == 1 {
		var lastLine string
		for _, line := range strings.Split(logTail, "\n") {
			trimmed := strings.TrimRight(line, "\r")
			if strings.TrimSpace(trimmed) != "" {
				lastLine = trimmed
			}
		}

		if strings.HasPrefix(lastLine, ErrorSentinel) {
			trimmed := strings.TrimPrefix(lastLine, ErrorSentinel)
			return StateFailed, UnescapeSentinel(trimmed)
		}
		return StateFailed, "worker failed without a message"
	}

	return StateFailed, fmt.Sprintf("worker crashed (exit %d)", exitCode)
}
