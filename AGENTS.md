# AGENTS.md

ClosedVPS (`closedvps`) is a Go CLI that buys Hetzner Cloud servers behind firewalls with no inbound rules and sets them up
with cloud-init (Tailscale for SSH, unattended upgrades, optional Cloudflare Tunnel and Claude Code).

## Using it for a user

Buying a server costs the user money. Confirm the server type, location, price caps and count with them
first, and show `closedvps create --dry-run` before the real run. [skills/closedvps/SKILL.md](skills/closedvps/SKILL.md)
has the rest: install, secrets, flags, events and exit codes.

## Working on the code

[CONTRIBUTING.md](CONTRIBUTING.md) has the layout and the checks. On top of that:

- Never run `closedvps create` without `--dry-run` in tests or CI: it buys a real server. Tests use the fake
  API in `internal/hetznertest`.
- Hetzner calls go through the official hcloud-go client, wrapped in `internal/cloud`. Check
  https://docs.hetzner.cloud/ and its changelog before relying on a field: `server_types[].locations[].available
  == true` means orderable, whatever the reference text says.
- Anything that goes into cloud-init must pass `cloudinit.Config.Validate`, and a change to the setup shows up
  in `internal/cloudinit/testdata/*.yaml` (`go test ./internal/cloudinit -update` rewrites them).
- Facts users and agents rely on have one home each: flags, environment variables and exit codes in the
  command's help (`create.go`), events in `report.go` and the skill. `TestSkillMatchesHelp` checks the skill
  against them.
