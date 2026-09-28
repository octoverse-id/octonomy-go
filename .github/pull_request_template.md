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
- [ ] New/changed types stay faithful to `docs/openapi.yaml` (divergences documented)
- [ ] Exported symbols have doc comments
- [ ] Docs updated (README / `docs/`) if behavior or usage changed
- [ ] CHANGELOG `[Unreleased]` updated for user-facing changes
- [ ] No secrets, tokens, or tenant data committed

## Notes for reviewers
<!-- New endpoints covered, follow-ups, or anything reviewers should focus on. -->
