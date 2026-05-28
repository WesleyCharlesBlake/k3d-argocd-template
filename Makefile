# k3d + ArgoCD local development template — bootstrap, dev-loop, and operability targets

REPO_URL         ?= https://github.com/WesleyCharlesBlake/k3d-argocd-template.git
ARGOCD_VERSION   ?= v3.0.0
ARGOCD_NAMESPACE ?= argocd
CLUSTER_NAME     ?= k3d-argocd-template
IMAGE_REPO       ?= ghcr.io/wesleycharlesblake/k3d-argocd-template-myapp
GIT_SHORT_SHA    := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DEV_TAG          := dev-$(GIT_SHORT_SHA)

.PHONY: help
help:
	@echo "Bootstrap:"
	@echo "  bootstrap         — full setup: cluster + ArgoCD + (optional creds) + root Application"
	@echo "  cluster-create    — create the k3d cluster only"
	@echo "  cluster-destroy   — delete the k3d cluster"
	@echo "  install-argocd    — install ArgoCD into the cluster"
	@echo "  argocd-repo-creds — register GitHub PAT for ArgoCD (only needed for private forks)"
	@echo "  bootstrap-apps    — apply the root ArgoCD Application (app-of-apps)"
	@echo "  destroy           — alias for cluster-destroy"
	@echo ""
	@echo "Image cache (skip network on recreate; ~3-4 min faster):"
	@echo "  snapshot-images   — capture current cluster image set → bootstrap/images.txt"
	@echo "  pre-images        — import bootstrap/images.txt into the k3d cluster"
	@echo ""
	@echo "Dev loop (build locally, skip the GHCR roundtrip):"
	@echo "  dev-image         — build app image locally, import into k3d ($(DEV_TAG))"
	@echo "  dev-deploy        — dev-image + bump charts/myapp/values.yaml; ready to commit"
	@echo "  dev-sync          — force ArgoCD to sync myapp now (skip the ~3m poll)"
	@echo ""
	@echo "Operations:"
	@echo "  argocd-ui         — port-forward the ArgoCD UI to https://localhost:8080"
	@echo "  argocd-password   — print the initial admin password"
	@echo "  app-url           — print the URL where the workload is reachable"

# ─────────────────────────────────────────────────────────────────────
# Bootstrap
# ─────────────────────────────────────────────────────────────────────

# Public repos don't require credentials; ArgoCD can fetch anonymously.
# For private forks, set GITHUB_PAT and the bootstrap will register repo creds.
.PHONY: bootstrap
bootstrap:
	@$(MAKE) cluster-create
	@$(MAKE) pre-images
	@$(MAKE) install-argocd
	@if [ -n "$$GITHUB_PAT" ]; then \
	  $(MAKE) argocd-repo-creds; \
	else \
	  echo "ℹ️  No GITHUB_PAT set; skipping ArgoCD repo credentials registration."; \
	  echo "   (Required only when forking this repo as private.)"; \
	fi
	@$(MAKE) bootstrap-apps
	@echo ""
	@echo "✅ Bootstrap complete."
	@echo ""
	@echo "  ArgoCD UI:       make argocd-ui     (then https://localhost:8080)"
	@echo "  Admin password:  make argocd-password"
	@echo "  Workload URL:    make app-url"

.PHONY: cluster-create
cluster-create:
	@bash bootstrap/00-cluster-create.sh
	@echo "Waiting for nodes to be Ready..."
	@kubectl wait --for=condition=Ready node --all --timeout=120s

.PHONY: cluster-destroy
cluster-destroy:
	@k3d cluster delete $(CLUSTER_NAME)
	@echo "✅ Cluster $(CLUSTER_NAME) destroyed."

.PHONY: install-argocd
install-argocd:
	@echo "📦 Installing ArgoCD ($(ARGOCD_VERSION))..."
	kubectl apply -f bootstrap/argocd/namespace.yaml
	kubectl apply -n $(ARGOCD_NAMESPACE) \
		-f https://raw.githubusercontent.com/argoproj/argo-cd/$(ARGOCD_VERSION)/manifests/install.yaml
	@echo "Waiting for ArgoCD server..."
	kubectl wait --for=condition=available --timeout=300s \
		deployment/argocd-server -n $(ARGOCD_NAMESPACE)

# ArgoCD reads private repos by looking up Secrets in its namespace labelled
# argocd.argoproj.io/secret-type=repository. Only needed if you fork this
# template as a private repo. Public repos are fetched anonymously.
.PHONY: argocd-repo-creds
argocd-repo-creds:
	@test -n "$$GITHUB_PAT" || { \
	  echo "❌ GITHUB_PAT env var required."; \
	  echo "   Generate at https://github.com/settings/tokens (Contents: Read-only)"; \
	  echo "   Then: GITHUB_PAT=ghp_xxx make argocd-repo-creds"; \
	  exit 1; \
	}
	@echo "🔐 Registering GitHub repo credentials with ArgoCD..."
	@kubectl -n $(ARGOCD_NAMESPACE) create secret generic k3d-argocd-template-creds \
	  --from-literal=type=git \
	  --from-literal=url=$(REPO_URL) \
	  --from-literal=username=not-used \
	  --from-literal=password="$$GITHUB_PAT" \
	  --dry-run=client -o yaml | kubectl apply -f -
	@kubectl -n $(ARGOCD_NAMESPACE) label secret k3d-argocd-template-creds \
	  argocd.argoproj.io/secret-type=repository --overwrite
	@echo "✅ Repo credentials registered. ArgoCD can now fetch from $(REPO_URL)"

.PHONY: bootstrap-apps
bootstrap-apps:
	@echo "🚀 Applying root Application (app-of-apps)..."
	kubectl apply -f bootstrap/argocd/root-app.yaml
	@echo "✅ ArgoCD will now reconcile tooling and workload from $(REPO_URL)"

# ─────────────────────────────────────────────────────────────────────
# Image cache — pre-import all stack images into the k3d node so kubelet
# doesn't fetch from the internet during sync.
# ─────────────────────────────────────────────────────────────────────

.PHONY: snapshot-images
snapshot-images:
	@kubectl get pods -A \
	  -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{"\n"}{end}{range .spec.initContainers[*]}{.image}{"\n"}{end}{end}' \
	  | sort -u > bootstrap/images.txt
	@echo "✅ Captured $$(wc -l < bootstrap/images.txt | tr -d ' ') unique images → bootstrap/images.txt"
	@echo "   Commit this file so 'make pre-images' works for everyone."

.PHONY: pre-images
pre-images:
	@if [ ! -f bootstrap/images.txt ]; then \
	  echo "ℹ️  bootstrap/images.txt not present — skipping pre-load."; \
	  echo "   After this bootstrap settles, run 'make snapshot-images' and commit"; \
	  echo "   the file so the next 'make bootstrap' skips network image pulls."; \
	  exit 0; \
	fi
	@k3d cluster list | awk '{print $$1}' | grep -qx $(CLUSTER_NAME) || { \
	  echo "❌ Cluster $(CLUSTER_NAME) doesn't exist yet. Run 'make cluster-create' first."; \
	  exit 1; \
	}
	@total=$$(grep -cv '^$$' bootstrap/images.txt); \
	echo "📥 Phase 1/2: ensuring $$total images are in local Docker cache..."; \
	missing=0; cached=0; \
	while IFS= read -r img; do \
	  [ -z "$$img" ] && continue; \
	  if docker image inspect "$$img" >/dev/null 2>&1; then \
	    cached=$$((cached+1)); \
	  else \
	    missing=$$((missing+1)); \
	    printf "  ⬇  %s\n" "$$img"; \
	    docker pull -q "$$img" >/dev/null || echo "  ⚠  pull failed for $$img (cluster will retry)"; \
	  fi; \
	done < bootstrap/images.txt; \
	echo "    $$cached cached, $$missing pulled."
	@echo "📦 Phase 2/2: batch-importing all images into k3d (single tarball)..."
	@grep -v '^$$' bootstrap/images.txt | xargs k3d image import -c $(CLUSTER_NAME)
	@echo "✅ Image cache primed — bootstrap will skip network pulls."

.PHONY: destroy
destroy: cluster-destroy

# ─────────────────────────────────────────────────────────────────────
# Dev loop — build + import to k3d, no GHCR push needed
# ─────────────────────────────────────────────────────────────────────

.PHONY: dev-image
dev-image:
	@echo "📦 Building $(IMAGE_REPO):$(DEV_TAG)"
	docker build \
	  --build-arg VERSION=$(DEV_TAG) \
	  --build-arg COMMIT=$(GIT_SHORT_SHA) \
	  -t $(IMAGE_REPO):$(DEV_TAG) \
	  app/
	@echo "📥 Importing into k3d cluster '$(CLUSTER_NAME)'..."
	k3d image import $(IMAGE_REPO):$(DEV_TAG) -c $(CLUSTER_NAME)
	@echo ""
	@echo "✅ Image $(IMAGE_REPO):$(DEV_TAG) is in the cluster."
	@echo ""
	@echo "To deploy via GitOps:"
	@echo "  make dev-deploy    # bumps values.yaml, then commit + push"

.PHONY: dev-deploy
dev-deploy: dev-image
	@command -v yq >/dev/null 2>&1 || ( \
	  echo "❌ yq is required."; \
	  echo "   macOS: brew install yq"; \
	  echo "   Linux: sudo snap install yq  OR  https://github.com/mikefarah/yq#install"; \
	  exit 1 \
	)
	@echo "📝 Updating charts/myapp/values.yaml → image.tag: $(DEV_TAG)"
	yq -i '.image.tag = "$(DEV_TAG)"' charts/myapp/values.yaml
	@echo ""
	@echo "✅ values.yaml updated. Review with: git diff charts/myapp/values.yaml"
	@echo ""
	@echo "Next:"
	@echo "  git add charts/myapp/values.yaml"
	@echo "  git commit -m 'dev: bump myapp image to $(DEV_TAG)'"
	@echo "  git push"
	@echo "  → ArgoCD reconciles within ~3 min, OR run 'make dev-sync' to force it now"

.PHONY: dev-sync
dev-sync:
	@command -v argocd >/dev/null 2>&1 || ( \
	  echo "❌ argocd CLI required. Install: brew install argocd  OR  https://argo-cd.readthedocs.io/en/stable/cli_installation/"; \
	  exit 1 \
	)
	@echo "🔄 Forcing ArgoCD to sync myapp..."
	argocd app sync myapp
	@echo "✅ Sync triggered."

# ─────────────────────────────────────────────────────────────────────
# Operability
# ─────────────────────────────────────────────────────────────────────

.PHONY: argocd-ui
argocd-ui:
	@echo "Open https://localhost:8080 (accept self-signed cert; user: admin)"
	kubectl port-forward -n $(ARGOCD_NAMESPACE) svc/argocd-server 8080:443

.PHONY: argocd-password
argocd-password:
	@kubectl -n $(ARGOCD_NAMESPACE) get secret argocd-initial-admin-secret \
		-o jsonpath='{.data.password}' | base64 -d
	@echo ""

.PHONY: app-url
app-url:
	@echo "App is reachable at: http://myapp.localhost"
	@echo "(Add '127.0.0.1 myapp.localhost grafana.localhost' to /etc/hosts if it doesn't resolve)"
