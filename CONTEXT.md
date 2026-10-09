# kubrix-cli

A CLI that takes a user from a chosen kubriX target to a running kubriX platform, and later upgrades it.

## Language

**kubriX**:
The platform being installed.

**Platform repository**:
The git-hosted repository the CLI creates and bootstraps for a user, which Argo CD then applies.

**Install run**:
The act of taking a chosen target to a running kubriX platform: ready the platform repository, run the platform installer, report access.
_Avoid_: installation (for the act), deployment

**Installation**:
The saved record of an Install run, keyed by cluster name or kube context.
_Avoid_: install (for the record)

**Catalog**:
The user's saved Installations, most recently used first.

**Target**:
Where an Install run executes: a local kind cluster created by the CLI (a **Demo**), or an existing cluster chosen from a kubeconfig context.
_Avoid_: cluster type

**Platform installer**:
The Kubernetes Job that performs the platform install on the target cluster.
_Avoid_: installer job

**Bootstrap**:
Templating and pushing the platform repository, done either by the CLI or by the platform installer depending on what is being installed.

**Git host**:
The service that hosts platform repositories and pull requests.
_Avoid_: git provider, forge

**Upgrade**:
Merging a newer kubriX release into a branch of the platform repository, re-rendering changed templates, and opening a pull request.
