# kubrix-cli

A CLI that takes a user from a chosen kubriX target type to a running kubriX platform, upgrades it later, and explains and reviews the Helm values its applications are rendered with.

## Language

### Platform

**kubriX**:
The platform being installed.

**Platform repository**:
The git-hosted repository the CLI creates and bootstraps for a user, which Argo CD then applies. An upstream kubriX checkout has the same shape before it is bootstrapped.

**Target type**:
A kubriX profile, such as `kind` or `kubrix-oss-stack`, that decides which applications are installed and with which values.
_Avoid_: target (that is the cluster), stack, profile

**Target chart**:
The Helm chart in the platform repository that turns a target type's values into one Argo CD Application per installed application.

**Bootstrap Application**:
The Argo CD Application of a target type that renders the target chart with that target type's values files.

**Application**:
One installed component of kubriX, deployed by Argo CD from its own chart in the platform repository.
_Avoid_: app (in prose), component

**Customer config**:
The answers a platform repository is bootstrapped with (cluster type, cloud provider, domain, size, …); templates are rendered with it.

### Installing and upgrading

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
_Avoid_: cluster type (that is a customer config answer)

**Demo**:
A Target that is a local kind cluster the CLI creates and can delete again.

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

### Values and diffs

**Values overlay**:
The ordered values files, inline values and parameters an Application is rendered with, exactly as Argo CD passes them to Helm.

**Assignment history**:
Every file, line or parameter that set a value, in merge order; the last one is effective.

**Effective value**:
The value an Application is rendered with after its whole values overlay is applied.

**Base**:
The revision a change is compared against, usually the pull request's base branch.
_Avoid_: target (for the base revision)

**Change**:
The revision being reviewed: a pull request's head, another revision, or the working tree.
_Avoid_: PR (when it is not a pull request), head (in prose)

**Test case**:
One set of values every changed chart is rendered with when diffing a change: a target type, or hand-picked chart values files and Helm arguments.

**Diff comment**:
A pull request comment that carries a diff's result and is updated or removed by the next diff of the same pull request.
