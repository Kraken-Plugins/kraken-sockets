<!-- PROJECT LOGO -->
<br />
<div align="center">
  <a href="https://kraken-plugins.com">
    <img src="resources/images/kraken.png" alt="Logo" width="128" height="128">
  </a>

<h3 align="center">Kraken Sockets</h3>

  <p align="center">
   An extended RuneLite API for creating plugins that support client interactions.
    <br />
</div>

# Kraken Sockets

`kraken-sockets` is a lightweight Go WebSocket server used by the Kraken socket plugin to coordinate room membership
and broadcast real-time messages between connected clients.

## Overview

The server accepts WebSocket connections, requires a `JOIN` packet as the first message, and then relays room membership updates and broadcast payloads to other clients in the same room.

Current packet headers:

- `JOIN`
- `LEAVE`
- `BROADCAST`

## Repository Layout

- `main.go` starts the HTTP and WebSocket server.
- `server/` contains the socket server implementation and tests.
- `manifests/socket/` contains the Helm chart used to deploy the service.
- `scripts/build.sh` builds and pushes a Docker image.
- `scripts/upgrade.sh` performs a Helm upgrade using the bundled chart values.
- `.github/workflows/ci.yml` defines the GitHub Actions CI/CD pipeline.

## Prerequisites

Before building or deploying, install the following tools:

- [Go](https://go.dev/doc/install)
- [Docker](https://docs.docker.com/get-docker/)
- [Helm](https://helm.sh/docs/intro/install/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)

If you plan to use the GitHub Actions deployment flow, you will also need:

- A Docker Hub repository for published images
- An Argo CD instance with access to the target cluster
- GitHub repository secrets configured for the workflow

## Local Development

### Run the server locally

Start the server directly with Go:

```bash
go run main.go -host 0.0.0.0 -port 8080
```

The server exposes:

- WebSocket endpoint: `ws://localhost:8080/`
- Health endpoint: `http://localhost:8080/healthz`

Clients must send a `JOIN` packet as the first message after connecting.

### Build the binary

Build the application binary locally:

```bash
go build -o main main.go
```

To cross-compile the same way CI does:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o main main.go
```

### Run tests

Execute the test suite from the repository root:

```bash
go test ./...
```

For verbose output:

```bash
go test -v ./...
```

## Docker Build and Publish

The repository includes a helper script for building and pushing a Docker image:

```bash
./scripts/build.sh <tag>
```

Example:

```bash
./scripts/build.sh 1.0.0
```

This script:

1. Builds the Docker image from `Dockerfile`
2. Tags it as `cbartram/kraken-sockets:<tag>`
3. Pushes it to Docker Hub

You must already be authenticated with Docker before running the script.

## Deployment

### Helm deployment

The repository ships with a Helm chart in `manifests/socket/`.

To deploy or upgrade the release using the included helper script:

```bash
./scripts/upgrade.sh
```

That script currently runs:

```bash
helm upgrade socket ./manifests/socket/ -f ./manifests/socket/values.yaml
```

For first-time installs, prefer the more explicit command below:

```bash
helm upgrade --install socket ./manifests/socket -f ./manifests/socket/values.yaml
```

### Kubernetes configuration

The default chart values configure:

- Release name: `socket`
- Kubernetes namespace: `kraken`
- Service type: `ClusterIP`
- Service port: `80`
- Container target port: `8080`
- Image repository: `cbartram/kraken-sockets`

Review and update [manifests/socket/values.yaml](manifests/socket/values.yaml) before deploying to another environment.

### WebSocket routing

The Helm chart only deploys the application `Deployment` and `Service`. Any ingress, gateway, or external routing is managed outside this repository.

If you expose the service through an ingress controller, API gateway, or load balancer, confirm that:

- WebSocket upgrades are enabled
- Idle connection timeouts are long enough for your clients
- TLS termination is configured correctly if using `wss://`

## GitHub Actions CI/CD

GitHub Actions is configured in [.github/workflows/ci.yml](.github/workflows/ci.yml).

### Workflow triggers

The workflow runs on pushes to:

- `main`
- `master`

### Build and test job

The `build-and-test` job:

- Runs in a `cimg/go:1.24.4` container
- Restores the Go module cache
- Downloads Go dependencies
- Builds the Linux AMD64 binary with `CGO_ENABLED=0`
- Runs `go test -v ./...`

### Deployment job

The `deploy-prod` job runs after tests pass and:

- Logs in to Docker Hub
- Builds and pushes the container image
- Tags images as:
  - `1.0.${{ github.run_number }}`
  - `latest`
- Installs the Argo CD CLI
- Logs in to Argo CD
- Updates the `kraken-sockets` Argo CD application image tag
- Syncs and waits for the deployment to complete

### Required GitHub Actions secrets

The current workflow expects these repository secrets:

- `GITHUB_USER`
- `GH_TOKEN`
- `DOCKER_USER`
- `DOCKER_TOKEN`
- `ARGOCD_USERNAME`
- `ARGOCD_PASSWORD`

If those secrets are missing or invalid, the workflow will fail during dependency download, image publishing, or deployment.

## Operational Notes

- The server listens on `0.0.0.0:8080` by default.
- The health check endpoint is `/healthz`.
- The WebSocket server uses ping/pong handling and read deadlines to detect stale connections.
- Abrupt disconnects without a WebSocket close frame will appear as abnormal closures in the logs.

## Built With

- [GoLang](https://go.dev/doc/install) - Programming Language

## Contributing

Please read [CONTRIBUTING.md](CONTRIBUTING.md) for details on contributing, expected behavior, and the process for submitting pull requests.

## Versioning

We use [Semantic Versioning](https://semver.org/) for versioning. For the versions available, see the [tags on this repository](https://github.com/cbartram/kraken-sockets/tags).

## Authors

- **cbartram** - *Initial Project implementation* - [RuneWraith](https://github.com/cbartram)

See also the list of [contributors](https://github.com/cbartram/kraken-sockets/contributors) who have participated in this project.

## License

This project is licensed under the [CC0 1.0 Universal](LICENSE) license. See the [LICENSE](LICENSE) file for details.

## Acknowledgments

- RuneLite for making an incredible piece of software and API.
