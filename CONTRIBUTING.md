# Contributing

Contributions, suggestions, and bug reports are welcome.

## What's in scope

This repo is a **template** — its main value is being small, readable, and easy to fork. Contributions that align with that scope:

- Bug fixes (broken bootstrap on a supported platform, manifest typos, CI failures)
- Documentation improvements (clarifications, fixing dead links, adding common-mistake notes)
- Compatibility updates (newer chart versions, newer k3d/k3s versions, newer Go versions)
- Small operational improvements that benefit the template directly (better defaults, clearer error messages)

## What's out of scope

Things that would make this less useful as a template:

- Additional workloads beyond `myapp` (the goal is to show one pattern clearly, not many)
- Tool swaps where the current choice is already a reasonable default (e.g. swapping Helm for Kustomize, ArgoCD for Flux). These are mentioned in the README's "Why these specific choices" section as alternatives.
- Production-grade hardening (TLS, network policies, signed images, RBAC tightening). These are listed in "Extending this template" as deliberate follow-ups for forks, not features of the template itself.

If you're not sure, open an issue first to discuss.

## Workflow

1. Fork the repo
2. Create a branch from `main`
3. Run `make destroy && make bootstrap` end-to-end on your branch — if anything breaks, fix it before opening the PR
4. Open a PR; the `build` and `argocd-diff-preview` workflows will run automatically
5. The diff-preview bot will post the rendered manifest diff as a PR comment

## Code style

- Go: standard `gofmt` / `go vet`. CI enforces both.
- YAML manifests: comments explain *why*, not *what* the YAML keys do.
- Markdown: prefer plain prose over Heavy Formatting.
- Shell: `set -euo pipefail` on top of every script.

## Releases

Releases are cut by pushing a semver tag matching `vMAJOR.MINOR.PATCH` to `main`. The `release.yml` workflow handles image build + GitHub Release creation.
