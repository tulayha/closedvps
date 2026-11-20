---
name: create
description: Buys Hetzner servers with closedvps after showing the plan and its price.
argument-hint: "[what it's for, type, location, count]"
disable-model-invocation: true
---

The user wants to buy servers with closedvps: $ARGUMENTS

Read ${CLAUDE_PLUGIN_ROOT}/skills/closedvps/SKILL.md and follow its "Buying servers" checklist from the top.
Treat what the user wrote above as their answers so far, and ask only what's still open.
