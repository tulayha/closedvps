# closedvps events

With `--json`, `create` and `delete` print one JSON event per line on stdout. The events:

- `plan`: `servers` (the names), `setup` (what first boot installs), `tailscale` (`key`, or `checked` and
  `open` with an OAuth client: `open` means their policy lets `tag:closedvps` reach their devices), `dry_run`,
  and `candidates` with `in_stock`.
- `waiting`: every check, with `bought` and `count`.
- `bought`: server, type, location, price, ipv4.
- `ready`: `ssh`, `tagged` (false means it joined without `tag:closedvps` and can reach their devices until
  they tag it), and `next`, the commands for the user to run.
- `unreachable`: a server that didn't join or doesn't answer SSH, with `reason`.
- `stopped`: the last event of any run that didn't finish, with `code`, `reason`, and `billing`, the servers
  bought that are running and costing money.
- `deleted`: from `delete`, with `server`, `firewall` and `price`.

Stop codes in `stopped`:

- `needs_key`: a secret or SSH key is missing; `reason` names the variable.
- `needs_yes`: no `--yes` without a terminal.
- `declined`: the user answered no in the terminal.
- `bad_token`: Hetzner or Tailscale refused the credentials, or the Hetzner token is read-only.
- `no_tag`: their Tailscale policy doesn't define `tag:closedvps` (OAuth client only).
- `too_small`: every type asked for is under 4 GB with `--claude` or `--codex`.
- `over_cap`: nothing fits the price caps, or `--max-total` is reached.
- `name_taken`: a server with that name exists.
- `usage`: a bad flag value.
- `interrupted`: stopped with Ctrl+C or a signal.
- `partial`: some servers bought, then it stopped.
- `untagged`, `unreachable`: bought, but a server joined untagged or didn't come up.
- `failed`: anything else; read `reason`.

How `create` ends, as `closedvps create --help` prints it:

```
Exit codes:
  0    every server bought and set up; when Tailscale runs on this computer (and without --no-wait),
       each one also answered SSH over your tailnet
  1    nothing bought
  3    something bought and billing, but not everything finished
  130  stopped with Ctrl+C before anything was bought
```

On a 3, tell the user which servers are billing (`billing` in `stopped`) and what to do: `closedvps delete
<name>`, or for `untagged`, the fix closedvps printed.
