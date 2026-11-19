# Explaining values with `kubrix values`

`kubrix values` shows how the ordered Helm values files of an application combine: the final values, which file and
line set each value, every earlier assignment, paths across applications, and suspicious overlays. It works on values
only; `kubrix diff` renders the charts.

## Quick start

`examples/values/` is a small project with a hand-written `.kubrix-values.yaml`. From that directory:

```bash
kubrix values apps      --target-type kind-hub-prime
kubrix values merge     --target-type kind-hub-prime --app mimir --show-sources
kubrix values explain   --target-type kind-hub-prime --app mimir mimir.distributor.replicas
kubrix values overrides --target-type kind-hub-prime --app mimir
kubrix values lint      --target-type kind-hub-prime --app mimir
kubrix values search    --target-type kind-hub-prime resources.requests.cpu
```

`explain` prints the effective value and every assignment in merge order:

```text
Application:      mimir
Chart:            mimir-distributed
Path:             mimir.distributor.replicas
Effective value:  3

History:
  1  mimir/values-kubrix-default.yaml:3  1  defined
  2  mimir/values-ha-enabled.yaml:3      3  overridden
  3  mimir/values-customer.yaml:4        3  effective
```

`merge --show-sources` writes the source above each value:

```yaml
mimir:
  distributor:
    # source: mimir/values-customer.yaml:4
    replicas: 3
    resources:
      requests:
        # source: mimir/values-size-medium.yaml:5
        cpu: 500m
```

## Where the values files come from

The first that applies:

1. **`--target-values FILE`**: a target values file of a kubriX target chart, layered over the target chart's
   defaults (`values-kubrix-default.yaml`).
2. **`--file FILE …` and `--set …`**: files in precedence order and Helm expressions, with `--app` naming the
   application (`--chart` its chart, by default the application's name). `--set` alone works too:

   ```bash
   kubrix values merge --app mimir --file defaults.yaml --file customer.yaml --set mimir.distributor.replicas=5 --show-sources
   kubrix values merge --app demo --set replicas=3 --set image.tag=v2
   ```
3. **`--target-type TYPE` with a `.kubrix-values.yaml`** in `--repo` (or `--values-config FILE`): the files the
   project config lists.
4. **`--target-type TYPE` in a kubriX platform repository** (`--repo`, by default the current directory): what
   Argo CD applies. `bootstrap-app-TYPE.yaml` lists the target chart's values files; the target chart is rendered
   with them, and each rendered Application gives its chart's values files (`default.valueFiles`, then its own),
   its inline `helm.values`/`helm.valuesObject`, and its parameters. Missing files are skipped, as with
   `ignoreMissingValueFiles`. A target type without a bootstrap Application layers `values-TYPE.yaml` over the
   target chart's defaults.

   Templates (`*.yaml.tmpl`) of a repository that is not bootstrapped yet, such as an upstream kubriX checkout, are
   rendered in memory with `bootstrap/customer-config.yaml`, or with `--customer-config FILE`, over the defaults of a
   plain k8s cluster. Their sources read `values-x.yaml.tmpl (rendered):12`.

```bash
kubrix values apps    --repo ../kubriX --target-type kubrix-oss-stack
kubrix values explain --repo ../kubriX --target-type kind --app grafana grafana.ingress.enabled
kubrix values apps    --repo ../kubriX --target-type kind --format json   # values files and parameters per application
```

### `.kubrix-values.yaml`

For repositories without a kubriX target chart, a project config lists the values files per target type:

```yaml
version: v1
targets:
  kind-hub-prime:
    applications:
      mimir:
        chart: mimir-distributed
        files:
          - mimir/values-kubrix-default.yaml
          - path: mimir/values-provider-azure.yaml
            optional: true
          - mimir/values-customer.yaml
```

Paths are relative to the config file. A missing required file, an application without files or without any existing
file is an error; a missing optional file is skipped; a file listed twice is merged twice, with a warning and a lint
finding.

## Merge and provenance model

A value is identified by target type, application, chart and path. Paths are dotted; a literal `.` or `\` in a key is
escaped as `\.` and `\\` (`labels.app\.kubernetes\.io/name`).

- Maps merge recursively; scalars, lists and `null` replace what was there. A list replaces the whole list.
- An explicit `null` stays an effective `null`; it does not delete the key.
- Changing between map, list, scalar and `null` is allowed and recorded.
- `--set` uses Helm's syntax (nested names, escaped dots, list indexes, list literals, booleans, numbers, strings,
  `null`) and applies after all files, in command-line order, as `--set:1`, `--set:2`, … An Application's parameters
  come before the user's `--set` and are named after the Application.
- Every assignment keeps its file, line and column; the last one is effective.

`--format json` of `apps`, `explain`, `overrides`, `lint` and `search` is stable output for scripts and CI.

## Lint rules

| Rule | Severity | Reported when |
|------|----------|---------------|
| `identical-redefinition` | info | a later file sets a value to what it already is (leaves only; a repeated map is reported by its leaves) |
| `type-change` | warning | a value changes between map, list, scalar and `null` |
| `list-replacement` | warning | a list replaces a different list; Helm does not merge list items |
| `duplicate-file` | warning | a file is listed twice |
| `no-op-file` | info | a file changes nothing in the result |

`--fail-on info|warning|error|never` (default `never`) makes `lint` exit with code 2 when a finding reaches that
severity, for CI.

## Limitations

- The values commands work on values files and parameters; they do not include the chart's own `values.yaml` or its
  subcharts' defaults. `kubrix diff` renders charts with Helm, including both.
- `--set-file` is not supported.
- Several assignments in one `--set` (`a=1,b=2`) share one source; repeat `--set` to tell them apart.
- YAML aliases are resolved, and output is normalized rather than keeping the original formatting.
