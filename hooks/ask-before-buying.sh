#!/bin/sh
# Reads Claude Code's PreToolUse JSON on stdin. A closedvps create that isn't a dry run, or a closedvps delete,
# goes to the user for approval; anything else passes untouched.
input=$(cat)
case $input in *closedvps*) ;; *) exit 0 ;; esac

reason=
if printf '%s' "$input" | grep -Eq 'closedvps[^|;&]* create' &&
	! printf '%s' "$input" | grep -Eq 'closedvps[^|;&]* create[^|;&]* (-n|--dry-run|-h|--help)( |\\|"|$)'; then
	reason="closedvps create buys servers, which costs money. Check the plan you agreed before approving."
elif printf '%s' "$input" | grep -Eq 'closedvps[^|;&]* delete' &&
	! printf '%s' "$input" | grep -Eq 'closedvps[^|;&]* delete[^|;&]* (-h|--help)( |\\|"|$)'; then
	reason="closedvps delete destroys servers and their disks. Check the names before approving."
fi
[ -n "$reason" ] || exit 0
printf '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"%s"}}\n' "$reason"
