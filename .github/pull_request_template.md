## Summary
<!-- What does this PR do and why? -->

Closes #

## Type of change
- [ ] Bug fix
- [ ] New feature (e.g. a new resource client or method)
- [ ] Refactor / chore
- [ ] Documentation
- [ ] Breaking change (changes an exported API). Per `docs/versioning.md` this needs a **major** once a line has shipped a stable release. The modern line is on `v2.0.0-rc.N`, which means **no further break is intended**: one that proves necessary **supersedes the candidate** (`rc.2`) rather than riding a bump, as it could while the line was on `v2.0.0-alpha.N`. Never a minor — minors are additive only

## Checklist
- [ ] **Base branch is right for the line.** `main` = `.../octonomy-go/v2` (Go 1.24+, active);
      `support/go1.13` = `.../octonomy-go` (Go 1.13, **security fixes only**). Getting this wrong on a
      release is unrecoverable — see `docs/release.md`
- [ ] No version bump in this PR — **unless this *is* the `release/vX.Y.Z` PR**, which is the one place `version.go` and the CHANGELOG release heading move
- [ ] `make fmt-check` and `go vet ./...` pass
- [ ] `make lint` passes (golangci-lint)
- [ ] `make test` passes (`go test -race`)
- [ ] `make examples` builds
- [ ] New/changed types stay faithful to the vendored contracts — `docs/openapi-v2.yaml` (`/api/v2`) and `docs/openapi.yaml` (`/api/v1`) — with any divergence documented
- [ ] Exported symbols have doc comments
- [ ] Docs updated (README / `docs/`) if behavior or usage changed
- [ ] CHANGELOG `[Unreleased]` updated for user-facing changes
- [ ] No secrets, tokens, or tenant data committed

## Notes for reviewers
<!-- New endpoints covered, follow-ups, or anything reviewers should focus on. -->
