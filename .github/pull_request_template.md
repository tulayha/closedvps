## What changes

## How it was checked

- [ ] `go test -race ./...` and `golangci-lint run`
- [ ] if the first-boot setup changed: `go test ./internal/cloudinit -update` and the diff in `testdata/` reviewed
- [ ] if flags, exit codes or events changed: the skill (`skills/closedvps/SKILL.md`) updated
