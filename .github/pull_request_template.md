## Summary
<!-- What does this PR do and why? -->

Closes #

## Type of change
- [ ] Bug fix
- [ ] New feature (e.g. a new resource client or method)
- [ ] Refactor / chore
- [ ] Documentation
- [ ] Breaking change (changes an exported API, in the sense `docs/versioning.md` defines — an added struct field is not one) — **not admissible on this line**: it can never publish a major, so a break ships on `main` instead

## Checklist
- [ ] `make fmt-check` and `go vet ./...` pass
- [ ] `make lint` passes (golangci-lint)
- [ ] `make test` passes (`go test -race`)
- [ ] `make examples` builds
- [ ] New/changed types stay faithful to the vendored contracts — `docs/openapi.yaml` (`/api/v1`) and `docs/openapi-v2.yaml` (`/api/v2`) — with any divergence documented, `docs/contract-coverage.yaml` updated for any operation this PR implements, and its driver in `tools/contractdrift/drivers.go` setting every new field (`make contract-check` passes)
- [ ] Exported symbols have doc comments
- [ ] Docs updated (README / `docs/`) if behavior or usage changed
- [ ] CHANGELOG `[Unreleased]` updated for user-facing changes
- [ ] No secrets, tokens, or tenant data committed

## Notes for reviewers
<!-- New endpoints covered, follow-ups, or anything reviewers should focus on. -->
