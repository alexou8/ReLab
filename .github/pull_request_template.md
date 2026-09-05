## What this changes, and why

<!-- The why in prose: what was decided, and what was rejected. If a test found
     a real bug, say what the symptom was and why this fix is right. -->

## Verification

<!-- Tick what you ran. Leave unticked anything you could not run, and say why —
     an honest gap is more useful than an assumed pass. -->

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...`
- [ ] `golangci-lint run ./...` is clean
- [ ] `go mod tidy` leaves `go.mod` and `go.sum` unchanged
- [ ] `make check`
- [ ] `make scenarios` — required if this touches recovery, scheduling, faults or replay
- [ ] `make crash-tests` — required if this touches crash recovery
- [ ] `cd web && npx tsc --noEmit && npm run build` — required for a dashboard change

## Guarantees

- [ ] This change does not weaken a guarantee in `docs/reliability.md`, or it does and says so below with a decision record
- [ ] Any new claim in the README, the docs or the dashboard is backed by a test or a measurement, and names it
- [ ] Documentation affected by this change (`DATA.md`, `ARCHITECTURE.md`, `SECURITY.md`, `docs/reliability.md`) is updated in the same commit
- [ ] A schema change adds a new migration and does not edit a released one

## Anything reviewers should look at first

<!-- The part you are least sure about. -->
