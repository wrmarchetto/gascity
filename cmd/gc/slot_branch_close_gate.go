package main

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

// reusableSlotBranchPattern identifies the branch shape assigned to a reusable
// worker slot. Such branches are intentionally outside the merge order's
// eligible prefixes, so a close reason that claims one would strand the work.
// The final numeric segment is the session suffix; requiring it avoids
// mistaking ordinary prose such as "gc-work-record" for a branch.
var reusableSlotBranchPattern = regexp.MustCompile(`(?:^|[^[:alnum:]_./-])(gc-[[:alnum:]][[:alnum:]-]*-[0-9]{6,})(?:$|[^[:alnum:]_./-])`)

// closeReasonReusableSlotBranch returns the reusable slot branch named by a
// bd close reason, if any. Only the close subcommand accepts a close reason;
// update metadata is deliberately ignored because it cannot claim a landing
// branch through this contract.
func closeReasonReusableSlotBranch(bdArgs []string) string {
	reason := bdCloseReason(bdArgs)
	match := reusableSlotBranchPattern.FindStringSubmatch(reason)
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// rejectReusableSlotBranchClose emits the close-contract refusal when a
// reusable slot branch is claimed as the landing branch. It runs before city
// resolution, so an invalid close cannot fall through merely because the
// configured store is temporarily unavailable.
func rejectReusableSlotBranchClose(bdArgs []string, stderr io.Writer) bool {
	branch := closeReasonReusableSlotBranch(bdArgs)
	if branch == "" {
		return false
	}
	fmt.Fprintf(stderr, "gc bd: refusing close: reason names reusable slot branch %q; commit work on a per-bead feat/, fix/, harden/, or docs/ branch instead\n", branch) //nolint:errcheck // best-effort stderr
	return true
}

// bdCloseReason returns the final inline --reason/-r value passed to bd close.
// It consumes all other known value flags so a flag-looking value cannot be
// mistaken for a reason. A reason-file is intentionally not read: the normal
// close contract uses an inline reason, and an unreadable external file cannot
// prove where a close claims work landed.
func bdCloseReason(bdArgs []string) string {
	if len(bdArgs) == 0 || bdArgs[0] != "close" {
		return ""
	}
	valueFlags := bdSubcmdValueFlags("close")
	var reason string
	for i := 1; i < len(bdArgs); i++ {
		arg := bdArgs[i]
		if arg == "--" {
			break
		}
		if value, ok := strings.CutPrefix(arg, "--reason="); ok {
			reason = value
			continue
		}
		if value, ok := strings.CutPrefix(arg, "-r="); ok {
			reason = value
			continue
		}
		if arg == "--reason" || arg == "-r" {
			if i+1 >= len(bdArgs) {
				return ""
			}
			i++
			reason = bdArgs[i]
			continue
		}
		if !strings.Contains(arg, "=") && valueFlags[arg] && i+1 < len(bdArgs) {
			i++
		}
	}
	return reason
}
