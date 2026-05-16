# myapp

An example Go service backed by Postgres. Demonstrates a stateful workload deployed via GitOps with the operability signals an SRE expects.

## Endpoints

| Method | Path       | Purpose |
|---|---|---|
| GET | `/healthz` | Liveness — 200 if process is alive (does **not** depend on DB) |
| GET | `/readyz`  | Readiness — 200 only when startup is done **and** DB is reachable |
| GET | `/metrics` | Prometheus exporter |
| GET | `/items`   | List items |
| POST | `/items`  | Create an item (`{"name": "..."}`) |

## Configuration (env vars)

| Var | Default | Notes |
|---|---|---|
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `DB_URL` | (required) | Postgres DSN, e.g. `postgres://myapp:changeme@host:5432/myapp?sslmode=disable` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `SHUTDOWN_GRACE` | `10s` | How long to wait for in-flight requests on SIGTERM |

## Run locally (without Kubernetes)

```bash
# Start a Postgres for testing:
docker run -d --rm --name pg \
  -e POSTGRES_PASSWORD=changeme -e POSTGRES_USER=myapp -e POSTGRES_DB=myapp \
  -p 5432:5432 postgres:16-alpine

DB_URL='postgres://myapp:changeme@localhost:5432/myapp?sslmode=disable' \
  go run .
```

In another terminal:

```bash
curl -sS http://localhost:8080/healthz
curl -sS http://localhost:8080/readyz
curl -sS http://localhost:8080/items
curl -sS -X POST http://localhost:8080/items \
  -H 'Content-Type: application/json' \
  -d '{"name":"first"}'
curl -sS http://localhost:8080/metrics | grep ^myapp
```

## Build the image

```bash
VERSION=$(git describe --tags --always 2>/dev/null || echo dev)
COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo none)

docker build \
  --build-arg VERSION=$VERSION \
  --build-arg COMMIT=$COMMIT \
  -t ghcr.io/wesleycharlesblake/k3d-argocd-template-myapp:$VERSION \
  .

docker push ghcr.io/wesleycharlesblake/k3d-argocd-template-myapp:$VERSION
```

## Design notes

- **Single file** by intention — easier to read end-to-end in a template than scattered packages. Larger projects typically split into `cmd/`, `internal/handlers`, `internal/db`, etc.
- **Liveness ≠ readiness.** Liveness intentionally doesn't check the DB; the kubelet shouldn't restart pods because Postgres is temporarily unavailable. Readiness gates traffic, so when the DB is down the pod is removed from Service endpoints and load is shed gracefully.
- **Migrations run on startup** (idempotent SQL embedded via `embed`). A versioned migration tool (golang-migrate, goose) executed via a Helm pre-install Job is the common production pattern.
- **Graceful shutdown** flips readiness off before closing the server, giving the ingress controller time to drain traffic.
- **No router dependency** — Go 1.22+ `ServeMux` supports method-aware patterns. Less code, less surface area.
- **DB pool tuned modestly** (10 max, 1 min). Production tuning depends on observed load and DB capacity.
