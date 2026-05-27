# port/ — Internal Developer Platform as code

This directory is the **source of truth for the Port catalog** the same way `apps/` is the source of truth for ArgoCD. Everything is JSON / YAML, reviewed via PR, applied via `make port-sync`.

```
port/
├── blueprints/                  # entity schemas (the "types" in the catalog)
│   ├── k8sCluster.json
│   ├── service.json
│   ├── runningService.json
│   ├── deployment.json
│   └── argocdApplication.json
├── actions/                     # self-service actions (the "buttons")
│   ├── bump-image-tag.json      # the GitOps lever
│   ├── scaffold-service.json    # spawns a new chart + Application via PR
│   └── rollback.json            # git revert via PR
├── scorecards/                  # quality gates
│   └── production-readiness.json
└── mapping/
    └── k8s-exporter-config.yaml # cluster → catalog translation (canonical)
```

## How the pieces fit

- **Blueprints** = entity types. `Service` is the human concept (owned by a team, lives in a repo). `RunningService` is the live instance in a specific cluster. Separating them is what lets one `Service` appear once in the catalog but show up as `myapp-dev`, `myapp-staging`, `myapp-prod` running services — each with its own health, tag, scorecard.

- **Actions** = self-service buttons that invoke GitHub Actions workflows (`port-*` under `.github/workflows/`). They never touch the cluster directly — they open PRs against this repo, so the GitOps loop stays the single channel for change.

- **Scorecards** = checks against the entity's properties (set by the K8s exporter). `production-readiness.json` only references properties that the chart actually templates, so passing Gold means the chart is wired up correctly.

- **Mapping** = the JQ that turns a `Deployment` into a `runningService`, a `ServiceMonitor` into `hasServiceMonitor: true`, etc. The canonical copy lives here for review; an inlined copy lives in `apps/tooling/port-k8s-exporter.yaml` for the Helm chart to consume.

## Lifecycle

```
                 PR review                  make port-sync
port/*.json ─────────────────► main ──────────────────────► Port API
                                  │
                                  │  (port-k8s-exporter
                                  │   running in-cluster)
                                  ▼
                          live entity updates
                          (every ~30s + on K8s events)
```

Blueprint / action / scorecard changes go through PR review like any other infra change. Entity updates flow from the cluster automatically via the exporter — no human in the loop.

## Why JSON not Terraform

We *could* manage Port via [Terraform](https://registry.terraform.io/providers/port-labs/port-labs/latest). Reasons not to here:

- This repo's other infra (charts, ArgoCD Apps, manifests) is YAML/JSON-first. Adding Terraform pulls in HCL + a state backend for ~10 resources.
- The Port API is idempotent and accepts JSON directly. `make port-sync` is ~30 lines of `curl + jq`.
- For a real org with dozens of blueprints, hundreds of actions, and team-level RBAC, Terraform is the right answer — see `port-labs/terraform-provider-port-labs`.

## Adding a new self-service action

1. Drop the JSON under `actions/`.
2. Add the corresponding workflow under `.github/workflows/port-<name>.yml`. The contract is `workflow_dispatch` inputs that match the action's `workflowInputs` block.
3. `make port-sync` — Port picks up the new button immediately.

Pattern: every action ends in a PR. No action mutates the cluster directly. This keeps Port as a *trigger* layer and Git as the *truth* layer.
