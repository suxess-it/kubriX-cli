# kubrix CLI

`kubrix` sets up and maintains kubriX platforms in one interactive run:

- `kubrix demo`: a demo platform on a local [kind](https://kind.sigs.k8s.io/) cluster, and `kubrix demo delete` to remove it again.
- `kubrix install` (experimental): kubriX on an existing cluster (one of your kubeconfig contexts).
- `kubrix upgrade` (experimental): upgrades an installation to a newer kubriX release by opening a pull request in its repository.

`install` and `upgrade` are experimental: they are hidden, and refuse to run, unless you set `KUBRIX_EXPERIMENTAL=1`. The menu and `demo delete` then show installations on existing clusters as well; without it they only know demos.

They replace the manual steps: creating the GitHub repository, writing the `kubrix-install-secrets` Secret, running the installer job, and merging upstream releases by hand.

## Prerequisites

- **A GitHub token** for an account that belongs to at least one GitHub organization. The kubriX bootstrap only supports repositories owned by an organization. The token comes from one of:
  - an authenticated [`gh`](https://cli.github.com/) CLI (`gh auth login`), which is used automatically, or
  - a personal access token that you paste when prompted:

    | Token type | Required |
    |---|---|
    | Classic (recommended) | scopes `repo`, `read:org` and `workflow`; add `delete_repo` if you want `kubrix demo delete` to remove the repository |
    | Fine-grained | resource owner = your organization; **Administration**, **Contents** and **Workflows** set to read and write. A repository created after the token only works if the token has "All repositories" access. |

    The `workflow` scope is needed because the bootstrap pushes kubriX's `.github/workflows/` files to your repository. `gh auth login` doesn't grant it by default; add it with `gh auth refresh -h github.com -s workflow`.
- **`demo`:** Docker (Docker Desktop, or Docker Engine on Linux) with at least 4 CPUs and 16 GB of memory. With less, the tool warns and asks before continuing.
- **`install`:**
  - a kubeconfig context with cluster-admin rights;
  - a cluster with LoadBalancer support and a default StorageClass;
  - a domain you control that resolves publicly to the cluster, with port 80 reachable from the internet, because certificates come from Let's Encrypt.
- **`git`:** needed to install from a release and for `upgrade`.

You don't need `kind`, `kubectl`, `helm`, `curl`, `skopeo` or `gomplate`. The tool calls Docker directly and talks to Kubernetes and GitHub through their APIs.

## Build and run

```bash
cd cli
go run .                   # menu: demo, install, upgrade, delete, help
go run . demo              # or: go build -o kubrix . && ./kubrix demo
go run . install
go run . upgrade
```

`kubrix --version` prints the release, the commit and the build date. Release builds get them from linker flags (`-X main.version`, `-X main.commit`, `-X main.date`, set by GoReleaser and `container/Dockerfile`); a plain `go build` falls back to what the Go toolchain embeds.

Running `kubrix` without a command opens a menu in a terminal. When output is piped or there is no terminal, it prints the usage instead.

### Full-screen and plain mode

In a terminal, `kubrix` runs full-screen:
- a step list on the left;
- on the right, the current form or the scrolling output and installer logs;
- a status bar at the bottom.

Scroll with ↑/↓ and PgUp/PgDn. The first Ctrl-C cancels the running step; a second one quits immediately. When the run finishes, the screen stays open until you press `q`. After that, the messages (URLs, Argo CD login, CA commands, PR link) are printed to your terminal, and the whole run is saved to `last-run.log` in the config folder.

Add `--plain` to get the same flow printed line by line, for example `go run . demo --plain`. Plain mode is also used automatically when there is no terminal.

## kubriX versions

`demo` and `install` both ask which kubriX version to install.

- **Releases** (`v7.0.0` and newer): the CLI bootstraps the repository itself. It clones the release, writes `bootstrap/customer-config.yaml`, renders the templates and removes the deselected apps exactly like `install-platform.sh`, then pushes. The installer then only installs (`KUBRIX_BOOTSTRAP=false`). The CLI does this itself because the installer can't bootstrap from a tag: it pushes the tag instead of its bootstrap commit. The installer image is `kubrix-installer:<release>`.
- **Branches** (`main`, your checked-out branch, or any other branch): the installer bootstraps the repository itself, exactly as before.

Releases older than v7.0.0 are not offered. Their installer rendered templates differently (v6 also rendered `docs/*.md.tmpl`, v5 only selected values files).

## What `kubrix demo` does

1. If you have saved demos, asks whether to start a **new demo** or rerun a saved one. A new demo starts from your last answers, with a fresh repository name and a cluster name that no saved installation uses (`kubrix-demo`, `kubrix-demo-2`, …). Then it gets the GitHub token and checks it.
2. Detects **contributor mode**: when run inside a kubriX checkout, it defaults to installing the branch you have checked out from `origin`.
   - It warns you if the branch isn't pushed, differs from `origin`, or has uncommitted changes, because the installer only sees what is pushed.
   - If `origin` is a fork, the installer clones from the fork.
   - Outside a checkout, the default is the latest release.
3. Shows a form: organization, repository name, git user name, target type, kubriX source repository, version, and cluster name.
4. Creates the repository as **private** if it doesn't exist. If the repository already has content, it offers to reinstall from that content without bootstrapping.
   - **Fresh bootstrap:** you choose which applications to install.
     - The list comes from the target type's values file of the chosen version, rendered for the cluster type, with all apps pre-selected.
     - `traefik`, `cert-manager`, `argocd`, `external-secrets` and `openbao` are always installed.
     - Deselected apps are passed as `KUBRIX_APP_EXCLUDE` and removed from the repository during bootstrap.
     - Known dependencies are enforced: while `keycloak` is selected, `crossplane` and `cnpg` start selected, and the list can't be submitted without them. They are defined in `appDependencies` in `internal/install/apps.go`. Removing other apps is untested; an app that depends on a removed one may break.
   - **Reinstalling from an existing repository:** the app list is whatever that repository contains, so this step is skipped.
5. Picks the installer image. For a branch other than `main`, it uses `ghcr.io/suxess-it/kubrix-installer:<branch>` if that image exists; otherwise it asks before falling back to `latest`. Branch images are only built when someone runs the *create kubrix-installer image* workflow on the branch.
6. Creates the kind cluster. Its config mirrors `.github/kind-config.yaml` and maps ports 80 and 443 to localhost. The cluster is added to your kubeconfig. If the cluster already exists, you can recreate or reuse it.
7. Writes `kubrix-install-secrets`, applies `install-manifests.yaml` from the chosen version, and streams the installer job's logs until it finishes.
8. Prints the Argo CD login and the platform URLs (`https://<app>.127-0-0-1.nip.io`).

## What `kubrix install` does

1. Asks for the kubeconfig context (default: the current one) and checks it:
   - the cluster is reachable;
   - you are cluster-admin;
   - a default StorageClass exists (if not, it warns and asks before continuing).

   kind contexts are pointed to `kubrix demo`.
2. Shows a form: GitHub organization and repository, kubriX version (latest release by default), cloud provider (`on-prem` or `aks`), domain, and DNS. The target type is `kubrix-oss-stack`.
3. **DNS**, either:
   - a provider for external-dns:
     - Cloudflare, IONOS or STACKIT: you paste a token (plus a project ID for STACKIT);
     - AWS Route 53 or Azure DNS: you give the path to a credentials file;

     The CLI creates the Secret external-dns expects in the `external-dns` namespace. Credentials are never saved locally.
   - or **manual**: external-dns is not installed. As soon as traefik's LoadBalancer has an address, the CLI tells you which wildcard record (`*.<domain>`) to create.
4. Lets you choose the applications (as for the demo, rendered for a non-kind cluster).
5. Asks you to **type the context name** before anything is changed, because the installer gets cluster-admin rights there.
6. Creates and bootstraps the repository, creates the DNS Secret, runs the installer and streams its logs, and prints the Argo CD login and the platform URLs.

Size, HA and security-strict options are not offered: in OSS kubriX the values files they select don't exist.

## What `kubrix upgrade` does

1. Asks which installation to upgrade (a saved one, or any `owner/repo`), then clones it into a temporary directory together with kubriX upstream.
2. Reads the installed version from the repository's `.release-please-manifest.json`. If the file is missing, it asks.
3. Offers the newer releases, **at most one major version ahead**; repeat the upgrade for the next major version. In contributor mode it also offers `main`.
4. Shows the release notes. When the upgrade has breaking changes, the confirmation shows those sections, because they often need manual steps.
5. Merges the release into a branch `kubrix-upgrade-<version>`. **Generated files follow their templates:**
   - rendered outputs of templates that still exist are re-rendered with your `customer-config.yaml` where the release changed the template;
   - outputs of templates the release removed or moved are deleted;
   - your `customer-config.yaml` and your excluded apps are kept;
   - a conflict in any other file stops the upgrade without pushing anything, and lists the files to merge by hand.
6. Pushes the branch and opens a pull request describing the version change, release notes, and every regenerated, resolved or removed file. Argo CD applies the upgrade once you merge it.

Repositories bootstrapped with `KUBRIX_BOOTSTRAP_KEEP_HISTORY=false` share no history with kubriX and can't be upgraded this way.

## Saved files

They are stored in `~/.config/kubrix/` on Linux and macOS (`$XDG_CONFIG_HOME/kubrix/` if that is set) and in `%AppData%\kubrix\` on Windows:

- `installations.json`: every saved kind demo (keyed by cluster name) and cluster installation (keyed by context), with the answers used. It never contains tokens or DNS credentials.
- `kind-ca.crt`: the root CA that signs a kind demo's certificates.
- `last-run.log`: the full transcript of the last full-screen run, including the installer logs.

A kind demo is saved as soon as its cluster exists, and a cluster installation right before the installer starts, so `kubrix demo delete` also finds installs that failed.

## Browser trust and the CA warning

A kind demo's certificates are signed by a kind root CA that is built into the public `kubrix-installer` image. **Its private key is therefore public**: anyone can create certificates that a machine trusting this CA will accept. `kubrix demo` prints the command to import `kind-ca.crt` for your OS but never imports it automatically. Only import it on a throwaway or demo machine, and remove it afterwards. Installations on existing clusters use Let's Encrypt certificates instead.

## Cleaning up

```bash
go run . demo delete
```

It asks which saved installation to delete.

- **Kind demo:** it deletes the kind cluster.
- **Existing cluster:** it only forgets the installation; kubriX keeps running, and uninstalling is not supported.

After a separate confirmation, it can also delete the GitHub repository. That needs the `delete_repo` scope (with gh: `gh auth refresh -h github.com -s delete_repo`).

## Development

```bash
go vet ./... && go test ./...
```

No CI runs these yet. Several tests guard against drift from the rest of the repository; run `go test ./...` after changing any of these files:

- `.github/kind-config.yaml`: compared with `kindcluster.Config()`.
- `install-platform.sh`: its `customer-config.yaml` heredoc is compared with `render.CustomerConfig`.
- Any `*.yaml.tmpl`: all templates must render with the CLI's renderer, which supports gomplate's `strings.TrimSuffix` but no other gomplate functions.
- `platform-apps/target-chart/values-kubrix-oss-stack.yaml.tmpl`: the app lists for kind and k8s, and the dependency rules.

The upgrade tests run against a small fake upstream with real git (`internal/testrepo`).
