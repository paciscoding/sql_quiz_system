# Local Dev Workflow — fast frontend edits without rebuilding the nginx bundle

The production `docker-compose.yml` bakes the frontend into a static nginx image
(`frontend/Dockerfile` runs `bun run build`), so every UI tweak would otherwise need a
full image rebuild. For development, run the frontend with **Vite's dev server on the
host** instead — instant Hot Module Replacement — and let it proxy `/api` to the
backend that keeps running in Docker.

This works with **no component changes**: every API call in `src/` is a relative
`/api/v1/...` path, and the SSE exam stream (`useExamStream.ts`) uses a relative
`EventSource` URL — both flow through the one `/api` proxy configured in
`frontend/vite.config.ts`.

---

## Recommended: frontend on host, backend in Docker (zero infra changes)

The running `sql-quiz-frontend` nginx container already proxies `/api` (and the SSE
stream) to the backend over the Docker network. Vite just forwards to it.

```bash
# 1. Keep the Docker stack running (db + backend + the nginx frontend container).
docker compose up -d

# 2. Run the frontend dev server on the host (once: install deps).
cd frontend
bun install
bun run dev          # → http://localhost:5173  (HMR; proxies /api to nginx :80)
```

Open **http://localhost:5173** and edit anything under `frontend/src/` — changes appear
instantly, no rebuild. Ignore `:80` (the baked prod bundle) while developing.

- `vite.config.ts` proxies `/api` → `http://localhost` (the nginx container) by default.
- Because the browser only ever talks to `:5173`, requests are same-origin → no CORS.
- **When the backend changes** (e.g. while a new endpoint is being added), rebuild just
  that one service — your Vite session is unaffected:
  ```bash
  docker compose up -d --build backend
  ```

When you're done and want to verify the real production bundle, rebuild the frontend
image: `docker compose up -d --build frontend`, then check `http://localhost`.

---

## Optional: backend directly on the host for instant backend restarts

Use this when iterating heavily on Go code (skips the ~30–60 s backend image rebuild).
It needs Postgres and/or the backend published on the host, via the opt-in overlay
`docker-compose.dev.yml` (loopback-bound; never affects the tunnel deployment).

```bash
# Keep only Postgres in Docker, published on 127.0.0.1:5432
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d db

# Run the backend on the host against that DB (note: localhost, not the 'db' hostname)
cd backend
export $(grep -v '^#' ../.env | xargs)
export DATABASE_URL="postgres://postgres:secure_exam_password@localhost:5432/sql_quiz?sslmode=disable"
export ALLOWED_ORIGINS="http://localhost:5173"
go run cmd/main.go          # → http://localhost:8080

# Point the Vite proxy straight at it (separate terminal)
cd frontend
VITE_PROXY_TARGET=http://localhost:8080 bun run dev
```

Or keep the backend in Docker but publish it on the host (`...up -d db backend` with the
overlay) and use the same `VITE_PROXY_TARGET=http://localhost:8080`.

---

## Quick reference

| You want to… | Command |
| --- | --- |
| Edit frontend live | `cd frontend && bun run dev` → http://localhost:5173 |
| Apply a backend change (Docker) | `docker compose up -d --build backend` |
| Fast backend loop (host) | overlay `db` + `go run cmd/main.go` + `VITE_PROXY_TARGET=http://localhost:8080` |
| Verify the real prod bundle | `docker compose up -d --build frontend` → http://localhost |
| Type-check / tests | `cd frontend && bun run build` · `bun run test` |
