# GHA Webhook Scaler

A high-performance, webhook-driven auto-scaler for GitHub Actions self-hosted runners written in Go. This system provides instant scaling by responding to GitHub webhooks in real-time.

## 🚀 Features

- **Pure Webhook-Driven Scaling**: Instant response to GitHub `workflow_run.queued` events
- **Multi-Owner Support**: Manage runners for multiple GitHub users/organizations
- **Multi-Runtime Support**: Docker and Kubernetes container runtimes  
- **Multi-Architecture**: Supports AMD64 and ARM64 with intelligent scheduling (Kubernetes)
- **Webhook Management API**: Complete REST API for webhook lifecycle management
- **Token Validation**: Intelligent GitHub token scope detection and validation
- **Security**: HMAC signature verification and secure container management
- **Monitoring**: Built-in health checks, metrics, and status endpoints

## 📋 Prerequisites

- **Docker Runtime**: Docker and Docker Compose
- **Kubernetes Runtime**: Kubernetes cluster with kubectl access  
- GitHub Personal Access Token with `repo` scope
- GitHub repository with Actions enabled

## 🐳 Container Image

```bash
# Latest stable release
docker pull ghcr.io/cecil-the-coder/gha-webhook-scaler:latest

# Specific version  
docker pull ghcr.io/cecil-the-coder/gha-webhook-scaler:v1.0.0
```

**Supported Architectures:**
- `linux/amd64` (Intel/AMD 64-bit)
- `linux/arm64` (ARM 64-bit, Apple Silicon, AWS Graviton)

## 🔧 Quick Start

### Single Repository Mode

```bash
# Create .env file
cat > .env << EOF
GITHUB_TOKEN=your_github_personal_access_token
REPO_OWNER=your_github_username
REPO_NAME=your_repository_name
WEBHOOK_SECRET=your_webhook_secret_optional
MAX_RUNNERS=5
PORT=8080
DEBUG=false
EOF

# Start the auto-scaler
docker compose up -d
```

### All Repositories Mode

```bash
# Create .env file for all repositories under owner
cat > .env << EOF
GITHUB_TOKEN=your_github_personal_access_token
REPO_OWNER=your_github_username
ALL_REPOS=true
WEBHOOK_SECRET=your_webhook_secret_optional
MAX_RUNNERS=10
PORT=8080
DEBUG=false
EOF

# Start the auto-scaler
docker compose up -d
```

### Webhook Setup

#### Automatic Webhook Creation (Recommended)

The auto-scaler can automatically create and manage webhooks for your repositories:

**For Single Repository:**
```bash
# The webhook will be automatically created when the auto-scaler starts
# No manual webhook configuration needed!
```

**For All Repositories:**
```bash
# Set ALL_REPOS=true in your configuration
# The auto-scaler will automatically create webhooks for all accessible repositories
ALL_REPOS=true
```

**How it works:**
- Auto-scaler automatically creates webhooks pointing to `http://your-server:8080/webhook`
- Validates GitHub token permissions before webhook creation
- Skips repositories where you don't have admin/maintain permissions
- Updates existing webhooks if configuration changes
- Works with both single repositories and organization-wide setups

> **Note:** Webhooks are created automatically when the auto-scaler starts. Most users don't need to do anything beyond setting up their environment configuration.

#### Manual Webhook Configuration

If you prefer to create webhooks manually or the automatic setup doesn't work:

**For Single Repository:**
1. Go to Repository Settings → Webhooks → Add webhook
2. URL: `http://your-server:8080/webhook`
3. Content type: `application/json`
4. Secret: (optional, recommended)
5. Events: Select "Workflow runs"

**For All Repositories (Organization):**
1. Go to Organization Settings → Webhooks → Add webhook
2. Same configuration as above

## ⚙️ Configuration

### Environment Variables

| Variable | Description | Default | Required |
|----------|-------------|---------|----------|
| `GITHUB_TOKEN` | GitHub Personal Access Token | - | **Yes** |
| `REPO_OWNER` | Repository owner/organization | - | **Yes** |
| `REPO_NAME` | Repository name | - | **Yes** (unless `ALL_REPOS=true`) |
| `ALL_REPOS` | Enable all repositories under owner | `false` | No |
| `WEBHOOK_SECRET` | GitHub webhook secret | - | No |
| `MAX_RUNNERS` | Maximum concurrent runners | `5` | No |
| `RUNNER_TIMEOUT` | Maximum runner lifetime (seconds) | `3600` | No |
| `PORT` | HTTP server port | `8080` | No |
| `DEBUG` | Enable debug logging | `false` | No |
| `RUNTIME` | Container runtime (`docker` or `kubernetes`) | `docker` | No |
| `RUNNER_IMAGE` | Docker image for runners | `myoung34/github-runner:ubuntu-noble` | No |
| `CACHE_VOLUMES` | Enable caching volumes | `true` | No |
| `KUBERNETES_PVCS` | Use PersistentVolumeClaims for Kubernetes | `false` | No |
| `LABELS` | Additional runner labels (comma-separated) | - | No |

### Multiple Owner Configuration

Support for one or more GitHub owners/organizations:

```bash
# Specify one or more owners (comma-separated)
OWNERS=mycompany,personal,client-org

# Company organization
OWNER_MYCOMPANY_GITHUB_TOKEN=ghp_company_token
OWNER_MYCOMPANY_ALL_REPOS=true
OWNER_MYCOMPANY_WEBHOOK_SECRET=company_secret
OWNER_MYCOMPANY_MAX_RUNNERS=15
OWNER_MYCOMPANY_LABELS=company,production

# Personal account
OWNER_PERSONAL_GITHUB_TOKEN=ghp_personal_token
OWNER_PERSONAL_REPO_NAME=my-project
OWNER_PERSONAL_WEBHOOK_SECRET=personal_secret
OWNER_PERSONAL_MAX_RUNNERS=5

# Client organization
OWNER_CLIENT_ORG_GITHUB_TOKEN=ghp_client_token
OWNER_CLIENT_ORG_ALL_REPOS=true
OWNER_CLIENT_ORG_WEBHOOK_SECRET=client_secret
```

**Notes:**
- For a single owner, just list one owner: `OWNERS=mycompany`
- Owner names with hyphens are converted to underscores in environment variables (e.g., `my-org` becomes `OWNER_MY_ORG_GITHUB_TOKEN`)
- Legacy configuration using `GITHUB_TOKEN` and `REPO_OWNER` is still supported for backward compatibility


## 🔗 API Endpoints

### Core Endpoints

- `GET /health` - Health check
- `GET /version` - Version and build information
- `POST /webhook` - GitHub webhook receiver
- `GET /status` - Auto-scaler status and configuration
- `GET /metrics` - Operational metrics

### Webhook Management API

**Repository Webhooks:**
- `GET /api/v1/repos/{owner}/{repo}/hooks` - List webhooks
- `POST /api/v1/repos/{owner}/{repo}/hooks` - Create webhook
- `GET /api/v1/repos/{owner}/{repo}/hooks/{hook_id}` - Get webhook
- `DELETE /api/v1/repos/{owner}/{repo}/hooks/{hook_id}` - Delete webhook
- `POST /api/v1/repos/{owner}/{repo}/hooks/{hook_id}/test` - Test webhook
- `PUT /api/v1/repos/{owner}/{repo}/hooks/ensure` - Create or update webhook

**Token Validation:**
- `GET /api/v1/owners/{owner}/token/validate` - Validate token permissions
- `GET /api/v1/owners/{owner}/token/scopes` - Check token scopes
- `GET /api/v1/repos/{owner}/{repo}/token/validate` - Repository-specific validation

**Repository Management:**
- `GET /api/v1/owners/{owner}/repositories` - List accessible repositories
- `POST /api/v1/owners/{owner}/webhooks/preview` - Preview webhook setup
- `POST /api/v1/owners/{owner}/webhooks/setup` - Auto-setup webhooks

## 🏗️ Workflow Configuration

Use these labels in your GitHub Actions workflows:

```yaml
# For any architecture
runs-on: [self-hosted, linux, docker]

# Architecture-specific  
runs-on: [self-hosted, linux, x64, docker]     # AMD64
runs-on: [self-hosted, linux, arm64, docker]   # ARM64
```

## 🚢 Kubernetes Deployment

### Quick Deploy

```bash
# Update configuration in k8s-manifest.yaml
# - Update image registry
# - Set GitHub tokens and secrets
# - Configure domain for Ingress

kubectl apply -f k8s-manifest.yaml
```

### Key Kubernetes Features

- **Multi-Architecture Scheduling**: Automatically detects job requirements
- **Persistent Caching**: Uses PVCs for build cache persistence  
- **Resource Management**: Configurable CPU/memory limits
- **RBAC Security**: Proper service account permissions

```yaml
# Kubernetes-specific configuration
RUNTIME=kubernetes
CACHE_VOLUMES=true
KUBERNETES_PVCS=true  # Required for persistent caching
```

## 🔐 Security & Token Validation

### GitHub Token Requirements

**Classic Personal Access Tokens:**
- `repo` scope - Full repository access (recommended)
- `write:repo_hook` scope - Webhook management only

**Fine-Grained Personal Access Tokens:**
- "Repository webhooks" permission (read/write)
- "Actions" permission (write)

### Token Validation Features

The system automatically validates token permissions:

```bash
# Validate token for owner
curl http://localhost:8080/api/v1/owners/myorg/token/validate

# Check specific repository permissions
curl http://localhost:8080/api/v1/repos/myorg/myrepo/token/validate
```

### Webhook Security

- HMAC-SHA256 signature verification
- Owner-specific webhook secrets
- Repository authorization validation
- Request source validation

## 📊 How It Works

### Scaling Logic

1. **Webhook Reception**: Receives `workflow_run.queued` events from GitHub
2. **Repository Validation**: Ensures webhook is from authorized repository
3. **Owner Identification**: Determines which owner configuration to use
4. **Label Detection**: Analyzes queued jobs to determine required runner labels
5. **Capacity Check**: Ensures scaling won't exceed owner's max runner limit
6. **Runner Creation**: Starts new ephemeral runner with appropriate labels
7. **Automatic Cleanup**: Runners self-terminate after job completion

### Architecture Support

**Docker Runtime:**
- Runs on host architecture (AMD64 or ARM64)
- Uses Docker volumes for caching
- Direct Docker socket access

**Kubernetes Runtime:**  
- Multi-architecture scheduling based on workflow labels
- PersistentVolumeClaims for persistent caching
- Architecture-specific node selection

## 🛠️ Development

### Local Development

```bash
# Install dependencies
go mod download

# Run locally
export GITHUB_TOKEN=your_token
export REPO_OWNER=your_owner
export REPO_NAME=your_repo
go run *.go
```

### Building

```bash
# Build binary
go build -o autoscaler *.go

# Build Docker image
docker build -t github-actions-autoscaler:latest .
```

## 📈 Monitoring

### Built-in Monitoring

```bash
# Health check
curl http://localhost:8080/health

# Status and configuration  
curl http://localhost:8080/status

# Operational metrics
curl http://localhost:8080/metrics
```

### Example Status Response

```json
{
  "status": "healthy",
  "config": {
    "repository": "user/repo",
    "all_repos": false,
    "architecture": "amd64", 
    "max_runners": 5
  },
  "runners": {
    "current": 2,
    "max": 5,
    "list": [...]
  }
}
```

## 🚨 Troubleshooting

### Common Issues

**Webhooks not received:**
- Check firewall/port forwarding for port 8080
- Verify webhook URL is accessible from GitHub
- Check webhook secret configuration

**Runners not starting:**
- Verify GitHub token permissions using `/api/v1/owners/{owner}/token/validate`
- Check Docker socket access
- Review auto-scaler logs

**Rate limiting issues:**
- Use multiple tokens for different owners
- Monitor rate limits via token validation API

### Logs

```bash
# Docker Compose
docker logs -f github-runner-autoscaler-go

# Kubernetes
kubectl logs -f deployment/github-runner-autoscaler -n github-runners
```

## 🔄 Migration from Previous Versions

### Breaking Changes

This version is **webhook-driven only** and removes:
- Minimum runner guarantees
- Periodic queue monitoring  
- Activity-based scoring
- Complex distribution algorithms

### Migration Steps

1. **Update configuration** - Remove `MIN_RUNNERS` references
2. **Configure webhooks** - Set up webhook delivery (replaces polling)
3. **Verify operation** - Test with webhook delivery logs
4. **Update monitoring** - Scaling is now event-driven only

## 📄 License

This project is licensed under the MIT License.

---

**🎯 Key Design Principle:** Simple, reliable, webhook-driven scaling. One webhook event = one runner (up to configured limits).
