package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCloseReasonReusableSlotBranch(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "long reason flag names reusable slot branch",
			args: []string{"close", "ci-work", "--reason", "landed on gc-toolsmith-codex-3-032012076327"},
			want: "gc-toolsmith-codex-3-032012076327",
		},
		{
			name: "short reason flag names reusable slot branch",
			args: []string{"close", "ci-work", "-r", "landed on gc-toolsmith-codex-3-032012076327"},
			want: "gc-toolsmith-codex-3-032012076327",
		},
		{
			name: "feature branch remains attributable",
			args: []string{"close", "ci-work", "--reason=landed on fix/ci-work-close-gate"},
		},
		{
			name: "ordinary gc hyphenated prose is not a slot branch",
			args: []string{"close", "ci-work", "--reason", "updated gc-work-record docs"},
		},
		{
			name: "non close command is ignored",
			args: []string{"update", "ci-work", "--reason", "gc-toolsmith-codex-3-032012076327"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := closeReasonReusableSlotBranch(tc.args); got != tc.want {
				t.Fatalf("closeReasonReusableSlotBranch(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

func TestDoBdRefusesCloseOnReusableSlotBranch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := doBd([]string{
		"close", "ci-work", "--reason",
		"implemented on gc-toolsmith-codex-3-032012076327",
	}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("doBd(close slot branch) = %d, want 1; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "reusable slot branch") {
		t.Fatalf("refusal does not explain the branch hazard: %q", stderr.String())
	}
}
