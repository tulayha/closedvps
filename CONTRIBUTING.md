# Contributing

## Checks

```sh
go test -race ./...
golangci-lint run          # v2, config in .golangci.yml; `golangci-lint fmt` formats
go run . create --dry-run  # the plan and the first-boot setup, no token needed
```

`lefthook install` (after installing [lefthook](https://lefthook.dev)) runs the formatter, the linter and
gitleaks before each commit, and the tests before each push. CI runs all of that plus govulncheck, CodeQL,
short fuzz runs, `cloud-init schema` on the rendered setup, zizmor on the workflows, and the install scripts
on macOS, Linux and Windows.

Tests never touch Hetzner: `internal/hetznertest` is a fake of the API, and `closedvps create` without
`--dry-run` buys a real server, so it never runs in tests or CI.

Longer fuzz runs:

```sh
go test ./internal/cloudinit -run '^$' -fuzz '^FuzzRender$' -fuzztime 5m
go test ./internal/alert -run '^$' -fuzz '^FuzzParse$' -fuzztime 5m
go test ./internal/alert -run '^$' -fuzz '^FuzzCurlConfig$' -fuzztime 5m
```

A failing input lands in `testdata/fuzz/` and stays there as a test.

## Layout

| Where | What |
| --- | --- |
| `main.go` | the command tree and exit codes |
| `app.go` | what commands touch outside their logic (output, questions, clock, Hetzner); tests swap it |
| `create.go` | `closedvps create`, one method per step, with its help text (flags, environment, exit codes) |
| `commands.go`, `setup.go` | the other commands |
| `report.go` | `--json` events and alerts |
| `prefs.go`, `sshkey.go`, `tailscale.go`, `awake_*.go` | saved answers and secrets, the SSH key, the tailnet wait, keeping the computer awake |
| `internal/buy` | what may be bought under which caps, and the loop that buys it |
| `internal/cloud` | Hetzner through the official hcloud-go client |
| `internal/cloudinit` | the first-boot setup and the checks on what goes in it; `testdata/*.yaml` is the whole setup |
| `internal/alert` | alert links and the requests that deliver to them |
| `skills/closedvps` | the agent skill, also a Claude Code plugin (`claude plugin validate .`) |
| `install/` | install.sh and install.ps1, attached to each release and served at closedvps.com |

The website, closedvps.com, is on the `site` branch, which has its own README.

A change to the first-boot setup shows up as a diff in `internal/cloudinit/testdata/`: run
`go test ./internal/cloudinit -update` and read it. Flags, environment variables and exit codes live in the
help text in `create.go`; the skill repeats the exit codes and the events, and a test checks it does so word
for word.

## Releasing

Push a `v*` tag. The release workflow runs every CI check, then GoReleaser builds the archives, SBOMs and the
Docker image, and every artifact gets signed provenance.
