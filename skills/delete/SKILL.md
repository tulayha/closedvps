---
name: delete
description: Deletes closedvps servers after listing what each one costs and asking for each name.
argument-hint: "[server names]"
disable-model-invocation: true
---

The user wants to delete closedvps servers: $ARGUMENTS

Run `closedvps list --json` and show the user each server they named (or, with no names, every server and
leftover firewall closedvps made) with its price. Ask with AskUserQuestion which to delete, naming each one.
Then run `closedvps delete <names> --confirm <name, or how many> --json` and report each `deleted` event. Remind
them to remove the machines from their Tailscale admin too.
