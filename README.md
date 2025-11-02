# 🕵️‍♂️ You Are Officially Sus

Real-time party game where players ask questions, hunt the spy, and try to stay off the sus list. This Go-powered web app ships with a Postgres + Redis backend stack and container images ready for production.

## ✨ Features
- ⚡ Live lobby updates powered by Server-Sent Events and in-memory state
- 🧩 Hundreds of locations and social challenges baked in
- 🗳️ Multi-phase gameplay including ready checks, role reveal, and voting
- 🐳 Dockerfile + Compose setup for repeatable local environments
- 🚀 CI/CD workflows for testing, Docker image publishing, and tagged releases

## 🧱 Project Structure
- `main.go` – application entrypoint, HTTP handlers, SSE wiring, and game logic
- `templates/` – HTML templates rendered by the Go backend
- `static/` – CSS, JS, and other static assets
- `data/` – JSON datasets for locations and challenges
- `Dockerfile` – multi-stage build producing a lean distroless container image
- `compose.yml` – local development stack (app + Postgres + Redis)

## 🔧 Requirements
- Go 1.22+ (for local development)
- Docker & Docker Compose (or Docker Desktop)
- GitHub account with access to GitHub Container Registry (GHCR) for publishing release images

## 🧬 Environment Variables
| Variable      | Description                                             | Default     |
| ------------- | ------------------------------------------------------- | ----------- |
| `DB_PASSWORD` | Postgres password used by `docker compose` at startup    | _(required)_|
| `DEBUG`       | Enable verbose logging when set to any non-empty value  | _(empty)_   |

Create a local copy before running the stack:

```bash
cp .env.example .env
```

## 🚀 Quick Start
### Run with Go
```bash
go mod download
go run .
```
The server listens on `http://localhost:8080`.

### Run with Docker Compose
```bash
cp .env.example .env        # fill in DB_PASSWORD
docker compose up --build
```
The web UI is available at `http://localhost:3000`, Postgres on `5432`, and Redis on `6379`.

## 🧪 Testing
```bash
go test ./...
```
Continuous integration (`.github/workflows/ci.yml`) runs formatting checks, vetting, and tests on every push or pull request.

## 🛳️ Container Image
`docker build -t you-are-officially-sus:local .`

Published release images live under:
```
ghcr.io/<your-org-or-user>/you-are-officially-sus:<tag>
```

## 📦 Release Workflow
Tagging commits with the pattern `v*` (e.g., `v1.0.0`) triggers `.github/workflows/release.yml`:
1. Runs the Go test suite.
2. Builds and pushes Docker images to GHCR with the tag and `latest`.
3. Compiles a Linux AMD64 binary, archives it, and attaches it to a generated GitHub Release.

Make sure the repository (or organization) has [GHCR permissions](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry#configuring-access-control-and-visibility) configured so `GITHUB_TOKEN` can push images.

## 🤝 Contributing
1. Fork the repository & create a feature branch.
2. Run `gofmt` and `go test ./...` before pushing.
3. Open a pull request describing the change and gameplay impact.

## 📜 License
No explicit license has been provided yet. Reach out to the maintainers before using the code in closed-source or commercial projects.
