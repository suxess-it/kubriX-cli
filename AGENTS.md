# kubrix-cli

Go CLI (cobra) that installs, upgrades and explains kubriX platforms. The domain language is in
[CONTEXT.md](CONTEXT.md): name things with its terms, and add a term there the moment a new one settles.
Keep **Target** (a cluster) and **Target type** (a kubriX profile) apart in code, flags and prose.

## Hygiene

- **Layout.** `cmd/` is cobra wiring only: a `newXxxCmd` constructor and a `runXxx(ctx, …, svc)` flow whose
  outside world (GitHub, kind, chart repositories) sits in a services struct, so tests swap in fakes.
  Everything else lives in `internal/`, one package per concept.
- **Before handing off,** the tree is clean: `gofmt`, `go vet ./...`, `golangci-lint run` (default linters, no config
  file) and `go mod tidy` all leave nothing to fix. Best-effort terminal writes are `_, _ = fmt.Fprint…`, the
  repository's answer to errcheck.
- **Bugs are fixed on sight.** A bug, red check or lint finding you hit gets fixed in the change you are making,
  wherever it was caused; the tree you hand off is green.
- **Dead code goes in the same change** that makes it dead: unused helpers, flags, branches and test helpers.
- **Helm runs in-process** through the Helm v4 SDK; the CLI never shells out to `helm`. YAML that needs line numbers
  uses `go.yaml.in/yaml/v3`; other YAML uses `sigs.k8s.io/yaml`.
- **The user's repository is read-only.** Work that writes (dependency builds, checkouts of other revisions) happens in
  temporary copies or git worktrees that are removed afterwards.
- **Contracts are consumed elsewhere.** The JSON of `kubrix values` and the diff comments with their
  `<!-- gitops-diff:… -->` markers are read by scripts and kubriX CI; change them only as a deliberate,
  reviewed contract change.
- **Releases are generated.** PR titles are Conventional Commits (`feat:`, `fix:`, `chore:`, …), enforced by
  `semantic_pr.yaml`; release-please derives the version and changelog from them. `CHANGELOG.md`,
  `.release-please-manifest.json` and `Formula/` change only through the release workflow.
- **Experimental commands** are hidden and gated by `KUBRIX_EXPERIMENTAL=1` (`cmd/features.go`); a command
  graduates by dropping the gate.

## Testing

Read [docs/testing.md](docs/testing.md) before adding, moving or removing a test; it says which layer owns which
behaviour. The rules that apply to every change:

- Each behaviour is asserted at one layer, the lowest that can prove it; higher layers check only their own wiring.
- Standard `testing` only, table-driven where cases repeat, named by the behaviour they prove, fixtures in
  `t.TempDir()` built through `internal/testrepo`.
- A fix comes with a test that is **red** without it; say in the PR how you saw it fail.
- Golden files in `cmd/testdata` are contracts: after reviewing a deliberate change, rewrite them with
  `go test ./cmd -update`.
- Tests that need the real kubriX repository take it from `KUBRIX_REPO` (`testrepo.KubrixRepo`) and are skipped
  without it; CI sets it. Run `KUBRIX_REPO=<kubriX checkout> go test -race ./...` before handing off.
