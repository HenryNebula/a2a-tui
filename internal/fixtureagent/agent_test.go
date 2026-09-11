package fixtureagent

import (
	"strings"
	"testing"
	"time"
)

func TestParseScript(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		keyword string
		args    []string
	}{
		{"plain", "stream 5", "stream", []string{"5"}},
		{"case fold", "  A2UI Form  ", "a2ui", []string{"Form"}},
		{"no args", "fail", "fail", nil},
		{"empty", "", "", nil},
		{"whitespace", "   ", "", nil},
		{"extra args", "slow 1.5 extra", "slow", []string{"1.5", "extra"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keyword, args := parseScript(tt.text)
			if keyword != tt.keyword {
				t.Errorf("keyword = %q, want %q", keyword, tt.keyword)
			}
			if len(args) != len(tt.args) {
				t.Fatalf("args = %v, want %v", args, tt.args)
			}
			for i := range args {
				if args[i] != tt.args[i] {
					t.Errorf("args[%d] = %q, want %q", i, args[i], tt.args[i])
				}
			}
		})
	}
}

func TestParseCount(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"missing", nil, 3},
		{"non-numeric", []string{"many"}, 3},
		{"in range", []string{"5"}, 5},
		{"clamped low", []string{"0"}, 1},
		{"clamped high", []string{"999"}, streamChunkLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCount(tt.args, 3, 1, streamChunkLimit); got != tt.want {
				t.Errorf("parseCount(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestParseSeconds(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{"missing uses default", nil, time.Second},
		{"fractional", []string{"0.25"}, 250 * time.Millisecond},
		{"negative falls back", []string{"-3"}, time.Second},
		{"garbage falls back", []string{"soon"}, time.Second},
		{"clamped to max", []string{"999999"}, maxDelay},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseSeconds(tt.args, time.Second); got != tt.want {
				t.Errorf("parseSeconds(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestHelpTextListsEveryKeyword(t *testing.T) {
	help := HelpText()
	for _, row := range KeywordHelp {
		if !strings.Contains(help, row.Keyword) {
			t.Errorf("HelpText missing keyword %q", row.Keyword)
		}
	}
}
