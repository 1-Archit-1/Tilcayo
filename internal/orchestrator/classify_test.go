package orchestrator_test

import (
	"github.com/1-Archit-1/Tilcayo/internal/orchestrator"
	"testing"
)

func TestUnescapeSentinel(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"plain error", "plain error"},
		{"line 1\\nline 2", "line 1\nline 2"},
		{"windows\\r\\nline", "windows\r\nline"},
		{"slash\\\\escaped", "slash\\escaped"},
		{"nul\\0byte", "nul\x00byte"},
		{"trailing backslash\\", "trailing backslash\\"},
		{"escaped backslash then n\\\\n", "escaped backslash then n\\n"},
	}

	for _, tc := range cases {
		got := orchestrator.UnescapeSentinel(tc.input)
		if got != tc.want {
			t.Errorf("UnescapeSentinel(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		exitCode   int
		oomKilled  bool
		logTail    string
		wantState  orchestrator.State
		wantErrMsg string
	}{
		{
			name:       "clean exit 0",
			exitCode:   0,
			wantState:  orchestrator.StateSucceeded,
			wantErrMsg: "",
		},
		{
			name:       "out of memory exit 137",
			exitCode:   137,
			oomKilled:  true,
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "out of memory",
		},
		{
			name:       "timeout exit 137",
			exitCode:   137,
			oomKilled:  false,
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "timed out",
		},
		{
			name:       "cancelled exit 143",
			exitCode:   143,
			wantState:  orchestrator.StateCancelled,
			wantErrMsg: "",
		},
		{
			name:       "worker error with sentinel and escaped newline",
			exitCode:   1,
			logTail:    "warnings on stderr\ntilcayo-error: syntax error\\nin line 1\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "syntax error\nin line 1",
		},
		{
			name:       "worker error without sentinel",
			exitCode:   1,
			logTail:    "Traceback (most recent call last):\nException: boom\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "worker failed without a message",
		},
		{
			name:       "sentinel followed by a later warning is ignored",
			exitCode:   1,
			logTail:    "tilcayo-error: boom\nWARNING: cgroup v1\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "worker failed without a message",
		},
		{
			name:       "last of two sentinel lines wins",
			exitCode:   1,
			logTail:    "tilcayo-error: fake echoed from user SQL\ntilcayo-error: real\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "real",
		},
		{
			name:       "exit 1 with empty tail",
			exitCode:   1,
			logTail:    "",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "worker failed without a message",
		},
		{
			name:       "CRLF endings and trailing blank lines",
			exitCode:   1,
			logTail:    "detail\r\ntilcayo-error: bad input\r\n\r\n\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "bad input",
		},
		{
			name:       "escaped NUL in message",
			exitCode:   1,
			logTail:    "tilcayo-error: a\\0b\n",
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "a\x00b",
		},
		{
			name:       "worker crashed exit 139",
			exitCode:   139,
			wantState:  orchestrator.StateFailed,
			wantErrMsg: "worker crashed (exit 139)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotState, gotErrMsg := orchestrator.Classify(tt.exitCode, tt.oomKilled, tt.logTail)
			if gotState != tt.wantState {
				t.Errorf("Classify() state = %v, want %v", gotState, tt.wantState)
			}
			if gotErrMsg != tt.wantErrMsg {
				t.Errorf("Classify() error = %q, want %q", gotErrMsg, tt.wantErrMsg)
			}
		})
	}
}
