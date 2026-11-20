---
name: closedvps
description: Buy and set up Hetzner Cloud servers with every inbound port closed (SSH over Tailscale, optional Cloudflare Tunnel) using the closedvps CLI. Use when the user wants a new Hetzner VPS or several, a safe or locked-down server, a server reachable only over Tailscale, or to keep trying to buy while Hetzner is sold out.
---

# closedvps

Buying a server costs the user money: agree on type, location, per-server cap, total cap and count with them,
and show `closedvps create --dry-run` output before the real run.

Install if `closedvps version` fails:

- macOS, Linux: `curl -fsSL https://closedvps.com/install.sh | sh`
- Windows: `irm https://closedvps.com/install.ps1 | iex`

`closedvps upgrade` updates a copy the install scripts put there. Your runs never get the daily "newer
release" note, since it only shows in a terminal and never with `--json` or `--yes`.

Ask the user to export these in the shell you use (don't put them in files or the chat):
`HCLOUD_TOKEN` (Hetzner, Read & Write; not needed if they saved one with `closedvps setup`), `TS_AUTHKEY`
(Tailscale, pre-approved, tagged `tag:closedvps` as the README's Tailscale access section sets up; one-off for
one server, reusable with a one-day expiry for `--count`), and optionally
`CLOUDFLARE_TUNNEL_TOKEN` and `CLOSEDVPS_NOTIFY` (alert links, if none are saved).

Without a terminal, `create` uses the user's saved answers from `closedvps setup`, or the defaults (nbg1,
fsn1, hel1; any shared type cheapest first; 10 a month per server; no total cap; no backups). Pass agreed
choices as flags.

Commands:

- `closedvps types`: prices and stock, read-only.
- `closedvps create --dry-run [options]`: the plan and the exact cloud-init, buys nothing.
- `closedvps create --yes --json [--count <n>] --type <t1:cap,t2> --location <l1,l2> --max-price <n> [--max-total <n>] [--backups] [--claude]`:
  buys the first in-stock matches, retrying every minute until all are bought. With `--count` above 1 the
  servers are named `<name>-1` to `<name>-<n>`.
- `closedvps list`: servers and whether each firewall is still closed.
- `closedvps open-ssh <name>` / `closedvps close-ssh <name>`: temporary SSH from the caller's IP when
  Tailscale is broken. Always close it afterwards.

`closedvps <command> --help` lists all options; `closedvps list --json` and `closedvps types --json` print
JSON.

## Watching a long run

A run can wait for hours, longer than a background command or a single watch lasts. Start it on its own,
writing events to a file, then watch the file:

```sh
nohup closedvps create --yes --json ... > closedvps-events.jsonl 2> closedvps.log &
```

Each line of `closedvps-events.jsonl` is one JSON event: `plan`, `waiting` (every check, with `bought` and
`count`), `bought` (server, type, location, price), `ready` (with `ssh`), `unreachable`, `stopped` (with
`reason`). Watch for `bought`, `ready`, `unreachable` and `stopped`, and also for the process exiting, so a
crash isn't read as still waiting. In Claude Code, a Monitor on
`tail -f closedvps-events.jsonl | grep --line-buffered -E '"event":"(bought|ready|unreachable|stopped)"'`
does that; re-arm it when it expires.

How `create` ends, as `closedvps create --help` prints it:

```
Exit codes:
  0    every server bought and set up; when Tailscale runs on this computer (and without --no-wait),
       each one also answered SSH over your tailnet
  1    nothing bought
  3    something bought and billing, but not everything finished
  130  stopped with Ctrl+C before anything was bought
```

On a 3, read the events and `closedvps list`, and tell the user which servers are billing.

The user gets alerts on their own (desktop, and their alert links), so don't send them duplicates; tell them
what happened when it matters, especially a 3.
