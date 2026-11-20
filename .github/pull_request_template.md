<!-- Title: "type: Subject", e.g. "feat: Ask for a tagged Tailscale key". feat and fix titles go into the
release notes word for word, so write them for people using closedvps. Types and rules: CONTRIBUTING.md. -->

## What changes

## How it was checked

- [ ] `go test -race ./...` and `golangci-lint run`
- [ ] if the first-boot setup changed: `go test ./internal/cloudinit -update` and the diff in `testdata/` reviewed
- [ ] if flags, exit codes or events changed: the skill (`skills/closedvps/SKILL.md`) updated
