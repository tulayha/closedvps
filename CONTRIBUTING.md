# Contributing

## Checks

```sh
go test -race ./...
golangci-lint run          # v2, config in .golangci.yml; `golangci-lint fmt` formats
go run . create -n --show-setup  # the plan and the first-boot setup, no token needed
```

`lefthook install` (after installing [lefthook](https://lefthook.dev)) runs the formatter, the linter and
gitleaks before each commit, and the tests before each push. CI runs all of that plus govulncheck, CodeQL,
short fuzz runs, `cloud-init schema` on the rendered setup, zizmor on the workflows, and the install scripts
on macOS, Linux and Windows.

Tests never touch Hetzner or Tailscale: `internal/hetznertest` and `internal/tsapi/tsapitest` are fakes of
their APIs, and `closedvps create` without `--dry-run` buys a real server, so it never runs in tests or CI.

Longer fuzz runs:

```sh
go test ./internal/cloudinit -run '^$' -fuzz '^FuzzRender$' -fuzztime 5m
go test ./internal/alert -run '^$' -fuzz '^FuzzParse$' -fuzztime 5m
go test ./internal/alert -run '^$' -fuzz '^FuzzCurlConfig$' -fuzztime 5m
```

A failing input lands in `testdata/fuzz/` and stays there as a test.

## Pull requests and release notes

PRs are squash-merged, so the PR title becomes the commit subject on `main`. Titles take the form
`type: Subject` with a capital after the colon and no scope:

| Type | For | In the release notes |
| --- | --- | --- |
| `feat` | something new people can use | under New |
| `fix` | something that was broken for people | under Fixes |
| `docs`, `site`, `test`, `ci`, `build`, `chore`, `refactor` | everything else | no |

A `feat` or `fix` title goes into the notes word for word after its type, so write it for someone using
closedvps: `feat: Warn when a new server joins the tailnet untagged`, not how the code changed. A CI check
fails a PR whose title doesn't fit. Commits pushed to `main` directly follow the same form.

A `v*` tag makes a draft release. Read its notes, fix any line that reads badly, then publish it; the install
scripts and `closedvps upgrade` keep the previous release until then.

## Layout

| Where | What |
| --- | --- |
| `main.go` | the command tree and exit codes |
| `app.go` | what commands touch outside their logic (output, questions, clock, Hetzner); tests swap it |
| `create.go` | `closedvps create`, one method per step, with its help text (flags, environment, exit codes) |
| `commands.go`, `setup.go` | the other commands |
| `ui.go` | how output looks: ✓ ! ✗ - lines, the spinner, errors with their fix |
| `report.go` | `--json` events and alerts |
| `prefs.go`, `sshkey.go`, `tailscale.go`, `awake_*.go` | saved answers and secrets, the SSH key, the tailnet wait, keeping the computer awake |
| `internal/buy` | what may be bought under which caps, and the loop that buys it |
| `internal/cloud` | Hetzner through the official hcloud-go client |
| `internal/tsapi` | Tailscale through its official Go client: one-off keys, the policy check, devices |
| `internal/cloudinit` | the first-boot setup and the checks on what goes in it; `testdata/*.yaml` is the whole setup |
| `internal/alert` | alert links and the requests that deliver to them |
| `skills/`, `hooks/` | the Claude Code plugin (`claude plugin validate .`): the `closedvps` skill other agents can load too, `/closedvps:create` and `/closedvps:delete`, and a hook that sends real buys and deletes to the user for approval |
| `install/` | install.sh and install.ps1, attached to each release and served at closedvps.com |

The website, closedvps.com, is on the `site` branch, which has its own README.

A change to the first-boot setup shows up as a diff in `internal/cloudinit/testdata/`: run
`go test ./internal/cloudinit -update` and read it. Flags, environment variables and exit codes live in the
help text in `create.go`; the skill repeats the exit codes and the events, and a test checks it does so word
for word. The stop codes are in the skill. `testdata/example-run.txt` is a whole run as a person sees it,
which the site's terminal is built from: `go test . -run TestExampleRun -update` rewrites it.

## Releasing

Push a `v*` tag. The release workflow runs every CI check, then GoReleaser builds the archives, SBOMs and the
Docker image, and every artifact gets signed provenance.
