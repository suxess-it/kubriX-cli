# Testing

Each behaviour is asserted at one primary layer: the lowest one that can prove it. Tests use the standard `testing`
package, are named by the behaviour they prove, build their fixtures in `t.TempDir()`, and share helpers through
`internal/testrepo`. A helper that no test uses is deleted.

## Layers

| Layer | Where | Owns |
|-------|-------|------|
| Pure logic | `*_test.go` next to the package | merge semantics and provenance (`values/engine`), lint rules (`values/lint`), path escaping, output formatting, project config and target-chart resolution, the PR comment Markdown and comment sync (`gitopsdiff`) |
| Rendering against git | `internal/gitopsdiff/diff_test.go` | revisions as worktrees, changed charts, Helm rendering, dependency builds and their cache, default values and value changes; offline, with `file://` subcharts or a fake dependency builder |
| Command wiring | `cmd/*_test.go`, in-process through `NewRoot` or the `runXxx` flows with fake services | flags, which source a flag selects, stdout versus stderr, exit codes, posting with `--pr`; not the semantics the layers below own |
| Contracts | `cmd/testdata/*.json`, the Markdown tests | the JSON of `kubrix values` and the PR comments; a change is a reviewed contract change (`go test ./cmd -update` rewrites the golden files) |
| Real repository | tests calling `testrepo.KubrixRepo(t)` | every target type of the kubriX checkout in `$KUBRIX_REPO` resolves to the applications the bootstrap installs, and a chart with remote dependencies renders; skipped without `KUBRIX_REPO`, run in CI |

## Running

```bash
go test ./...                                   # everything that needs no kubriX checkout
KUBRIX_REPO=../kubriX go test -race ./...       # as CI runs it; the kubrix diff check needs network for chart repositories
golangci-lint run
```

## Fixing and removing tests

A bug fix comes with a test that fails without the fix; say in the PR how you saw it fail. Fewer tests is fine, less
protection is not: a PR that removes or merges a test names the surviving test that asserts the same behaviour and
shows that it fails when the guarded line is reverted.
