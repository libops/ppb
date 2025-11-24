# proxy power button (ppb)

A reverse proxy that automatically powers on Google Compute Engine instances when traffic arrives.

This service is designed to run on **Google Cloud Run as an ingress layer** in front of your full application stack running on Google Compute Engine. When traffic hits your Cloud Run endpoint, PPB automatically powers on the target GCE VM (if needed) and proxies the request through. This architecture allows you to run complete application stacks on cost-effective VMs while only paying for Cloud Run when traffic actually arrives.

## Architecture

```
Internet → Cloud Run (PPB) → Google Compute Engine (Full App Stack + lightsout)
```

- **Cloud Run**: Runs PPB as serverless ingress, scales to zero when no traffic
- **GCE VM**: Runs your complete application (web server, database, etc.), can power off when idle
- **PPB**: Powers on the VM when requests arrive, proxies traffic through with IP authorization
- **lightsout**: Monitors activity and automatically shuts down VMs during idle periods (optional companion service)

### Complete On-Demand Infrastructure

PPB works seamlessly with [lightsout](https://github.com/libops/lightsout) to create a complete on-demand infrastructure solution:

- **PPB handles startup**: Automatically powers on GCE instances when traffic arrives
- **lightsout handles shutdown**: Monitors activity and automatically suspends instances during idle periods
- **Cost optimization**: Only pay for compute resources when actively serving traffic

Deploy lightsout alongside your application on the GCE instance to complete the automation cycle.

## Cloud Run Behavior

PPB runs as a reverse proxy with intelligent rate limiting:

1. **Request handling**: Each authorized request attempts to power on the target machine if needed
2. **Rate limiting**: Power-on attempts are rate limited with a configurable cooldown period (default 30s)
3. **API protection**: Prevents hammering Google Cloud APIs during high traffic periods
4. **IP Security**: Only requests from allowed IP ranges can trigger machine power-on attempts

This approach balances responsiveness with API efficiency - machines get powered on when traffic arrives, but multiple requests don't spam the Google Cloud APIs.

## Use Case Examples

- **Full Stack Applications**: Run complete LAMP/MEAN/Django stacks on GCE VMs with PPB as Cloud Run ingress
- **Development Environments**: Keep expensive GPU/high-memory dev instances off until developers access them
- **Legacy Applications**: Modernize access to monolithic applications that need full VM environments
- **Cost Optimization**: Run databases, processing servers, or complete application environments that can idle
- **Multi-service Backends**: Power on VMs running docker-compose stacks, Kubernetes clusters, or complex service meshes

## Config

Currently, only backend services running on Google Compute Engine are supported.

```yaml
type: google_compute_engine
port: 80
scheme: http
allowedIps:
  - 127.0.0.1/32
  - 10.0.0.0/8
  - 172.16.0.0/12
  - 192.168.0.0/16
ipForwardedHeader: X-Forwarded-For # header to check for real client IP
ipDepth: 0 # depth in X-Forwarded-For chain
powerOnCooldown: 30 # seconds between power-on attempts (default: 30)
# metadata about the machine that will be turned on
machineMetadata:
  project_id: foo
  zone: us-central1-a
  name: my-compute-instance-name
  usePrivateIp: false # true for VPC-native setups
# Optional proxy timeout configuration (defaults shown)
proxyTimeouts:
  dialTimeout: 120           # Connection dial timeout in seconds
  keepAlive: 120            # TCP keep-alive timeout in seconds
  idleConnTimeout: 90       # Idle connection timeout in seconds
  tlsHandshakeTimeout: 10   # TLS handshake timeout in seconds
  expectContinueTimeout: 1  # Expect: 100-continue timeout in seconds
  maxIdleConns: 100        # Maximum idle connections

```

### Configuration Reference

| Field                                   | Type     | Required | Default | Description                                                  |
|-----------------------------------------|----------|----------|---------|--------------------------------------------------------------|
| `type`                                  | string   | ✅       | -       | Backend type, currently only `google_compute_engine`         |
| `port`                                  | int      | ✅       | -       | Port on target machine to proxy to                           |
| `scheme`                                | string   | ✅       | -       | Protocol scheme (`http` or `https`)                          |
| `allowedIps`                            | []string | ✅       | -       | CIDR ranges of IPs allowed to access the proxy               |
| `ipForwardedHeader`                     | string   | ❌       | `""`    | Header to check for real client IP (e.g., `X-Forwarded-For`) |
| `ipDepth`                               | int      | ❌       | `0`     | Depth in forwarded header chain (0 = rightmost IP)           |
| `powerOnCooldown`                       | int      | ❌       | `30`    | Seconds between power-on attempts (rate limiting)            |
| `proxyTimeouts.dialTimeout`             | int      | ❌       | `120`   | Connection dial timeout in seconds                           |
| `proxyTimeouts.keepAlive`               | int      | ❌       | `120`   | TCP keep-alive timeout in seconds                            |
| `proxyTimeouts.idleConnTimeout`         | int      | ❌       | `90`    | Idle connection timeout in seconds                           |
| `proxyTimeouts.tlsHandshakeTimeout`     | int      | ❌       | `10`    | TLS handshake timeout in seconds                             |
| `proxyTimeouts.expectContinueTimeout`   | int      | ❌       | `1`     | Expect: 100-continue timeout in seconds                      |
| `proxyTimeouts.maxIdleConns`            | int      | ❌       | `100`   | Maximum number of idle connections                           |
| `machineMetadata.project_id`            | string   | ✅       | -       | Google Cloud project ID                                      |
| `machineMetadata.zone`                  | string   | ✅       | -       | GCE zone (e.g., `us-central1-a`)                             |
| `machineMetadata.name`                  | string   | ✅       | -       | GCE instance name                                            |
| `machineMetadata.usePrivateIp`          | bool     | ❌       | `false` | Use private IP for VPC-native setups                         |

Deploy this service on **Google Cloud Run** as the public endpoint for your application. Configure the `machineMetadata` to point to your GCE VM running the actual application stack. Only requests from allowed IPs will power on the VM and be proxied through. Set to `0.0.0.0/0` to allow any request to power on the machine.

### Environment Variables

PPB also supports these environment variables for runtime configuration:

| Variable                         | Description                                  | Default                  |
|----------------------------------|----------------------------------------------|--------------------------|
| `PPB_YAML`                       | YAML configuration content (takes priority)  | -                        |
| `PPB_CONFIG_PATH`                | Path to YAML configuration file              | /app/ppb.yaml            |
| `LOG_LEVEL`                      | Log level (`DEBUG`, `INFO`, `WARN`, `ERROR`) | `INFO`                   |
| `GOOGLE_APPLICATION_CREDENTIALS` | Path to service account JSON file            | Uses default credentials |

## IAM Permissions

The Google Service Account (GSA) used by Cloud Run needs a custom IAM role with minimal permissions to control the target compute instance.

### Custom Role Definition

Create a custom role with only the required permissions:

```hcl
resource "google_project_iam_custom_role" "compute-start" {
  project = var.project

  role_id     = "startVM"
  title       = "Start Compute Instance"
  description = "Minimal permissions for PPB to control compute instances"
  permissions = [
    "compute.instances.start",
    "compute.instances.resume",
    "compute.instances.get"
  ]
}
```

### Assign Role to Service Account

```hcl
resource "google_project_iam_member" "ppb_service_account" {
  project = var.project
  role    = google_project_iam_custom_role.compute-start.name
  member  = "serviceAccount:${var.service_account_email}"
}
```

### CLI Alternative

```bash
gcloud iam roles create startVM \
    --project=PROJECT_ID \
    --title="Start Compute Instance" \
    --description="Minimal permissions for PPB to control compute instances" \
    --permissions="compute.instances.start,compute.instances.resume,compute.instances.get"

gcloud projects add-iam-policy-binding PROJECT_ID \
    --member="serviceAccount:SERVICE_ACCOUNT_EMAIL" \
    --role="projects/PROJECT_ID/roles/startVM"
```

## Usage

### Local development

```bash
docker build -t us-docker.pkg.dev/your/gar/ppb:development .
docker run \
  -p 8080:8080 \
  -v $GOOGLE_APPLICATION_CREDENTIALS:/app/svc.json:ro \
  -v ./ppb.yaml:/app/ppb.yaml:ro \
  --env PPB_CONFIG_PATH=/app/ppb.yaml \
  --env GOOGLE_APPLICATION_CREDENTIALS=/app/svc.json \
  --env PRIVATE_IP=false \
  --env LOG_LEVEL=DEBUG \
  us-docker.pkg.dev/your/gar/ppb:development
```

PPB will act as a local ingress proxy to your GCE VM. Visit http://localhost:8080 and PPB will power on your target VM (if needed) then proxy your requests through to the application running on the VM.

### Google Cloud Run Deployment

Deploy PPB to Cloud Run and configure it as your application's public endpoint:

```bash
gcloud run deploy ppb \
  --image=us-docker.pkg.dev/your/gar/ppb:latest \
  --set-env-vars="PPB_CONFIG_PATH=/app/ppb.yaml" \
  --service-account=your-ppb-service-account@project.iam.gserviceaccount.com \
  --allow-unauthenticated \
  --port=8080 \
  --region=us-central1
```

Your users will access `https://ppb-xxx-uc.a.run.app` which automatically powers on your GCE VM and proxies traffic through.
