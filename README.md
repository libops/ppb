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
4. **Client filter**: Only explicitly allowed original-client CIDRs can trigger power-on after a configured, trusted proxy suffix is removed
5. **Bounded readiness retry**: Retries connection establishment while a VM or Direct VPC path becomes reachable, without adding application-level request retries

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
  - 127.0.0.1/32 # replace with an original-client CIDR for deployment
ipForwardedHeader: "" # direct peer address; configure only for a proven proxy chain
ipDepth: 0 # trusted proxy hops after the client in X-Forwarded-For
powerOnCooldown: 30 # seconds between power-on attempts (default: 30)
powerOnTimeout: 360 # total queued/API/instance-ready budget (default: 360)
# metadata about the machine that will be turned on
machineMetadata:
  project_id: foo
  zone: us-central1-a
  name: my-compute-instance-name
  usePrivateIp: false # true for VPC-native setups
# Optional proxy timeout configuration (defaults shown)
proxyTimeouts:
  dialTimeout: 120           # Total connection retry window in seconds
  dialAttemptTimeout: 5      # Timeout for one connection attempt in seconds
  dialRetryInterval: 1       # Delay between connection attempts in seconds
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
| `ipDepth`                               | int      | ❌       | `0`     | Trusted proxy hops after the client (0 selects rightmost IP)  |
| `powerOnCooldown`                       | int      | ❌       | `30`    | Seconds between power-on attempts (rate limiting)            |
| `powerOnTimeout`                        | int      | ❌       | `360`   | Total queued, API, and instance-ready budget in seconds       |
| `proxyTimeouts.dialTimeout`             | int      | ❌       | `120`   | Total connection retry window in seconds                     |
| `proxyTimeouts.dialAttemptTimeout`      | int      | ❌       | `5`     | Timeout for one connection attempt in seconds                |
| `proxyTimeouts.dialRetryInterval`       | int      | ❌       | `1`     | Delay between connection attempts in seconds                 |
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

Treat `ipForwardedHeader` and `ipDepth` as a proxy-chain contract, not an IP
parser convenience. PPB selects from the right edge so an attacker-supplied
prefix cannot replace the client hop. A missing header, malformed address, or
chain shorter than `ipDepth + 1` is denied; PPB never falls back to the proxy's
`RemoteAddr` when a trusted header is configured. Use depth `0` only for a
direct Cloud Run path whose hosted test proves Google appends the original
client last. Duplicate field lines are folded before suffix selection. An added
external Application Load Balancer normally adds a hop and requires a different
depth. Do not enable a forwarded header for an ingress path that has not proven
its appended suffix. Prefer Cloud Run IAM or a controlled load balancer/Cloud
Armor policy when caller authentication, rather than a wake-up filter, is
required. Before proxying, PPB replaces forwarding identity headers with the
single validated client address.

`proxyTimeouts.dialTimeout` bounds the complete TCP connection-establishment retry window. Each attempt is bounded by `dialAttemptTimeout`, with `dialRetryInterval` between failures. The readiness loop runs in the transport dialer before an HTTP connection exists; PPB does not add application-level request or status retries. Go's standard transport can retry requests it defines as replayable when a pooled connection is found stale. If the connection window expires, PPB returns `503 Service Unavailable` with `Retry-After: 5` so the client can make a deliberate retry. Request cancellation stops GCE polling, queued power-on work, and connection retry. HTTPS handshake readiness is outside the TCP retry loop and fails with the same retryable 503 response.

For Direct VPC egress, use a supported `/26` or larger subnet with sufficient free addresses, grant the Cloud Run service agent subnet use, and authorize the whole Cloud Run subnet CIDR at the VM firewall. Cloud Run addresses are ephemeral; never build the firewall around one revision address. PPB tolerates initial connection refusal and timeout within the configured retry window, but clients must still tolerate occasional connection resets after a connection has been established.

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
resource "google_compute_instance_iam_member" "ppb_service_account" {
  project       = var.project
  zone          = var.zone
  instance_name = var.instance_name
  role          = google_project_iam_custom_role.compute-start.name
  member        = "serviceAccount:${var.service_account_email}"
}
```

### CLI Alternative

```bash
gcloud iam roles create startVM \
    --project=PROJECT_ID \
    --title="Start Compute Instance" \
    --description="Minimal permissions for PPB to control compute instances" \
    --permissions="compute.instances.start,compute.instances.resume,compute.instances.get"

gcloud compute instances add-iam-policy-binding INSTANCE_NAME \
    --zone=ZONE \
    --project=PROJECT_ID \
    --member="serviceAccount:SERVICE_ACCOUNT_EMAIL" \
    --role="projects/PROJECT_ID/roles/startVM"
```

## Usage

### Local development

```bash
docker build -t us-docker.pkg.dev/your/gar/ppb:development .
docker run \
  --network=host \
  -v $GOOGLE_APPLICATION_CREDENTIALS:/app/svc.json:ro \
  -v ./ppb.yaml:/app/ppb.yaml:ro \
  --env PPB_CONFIG_PATH=/app/ppb.yaml \
  --env GOOGLE_APPLICATION_CREDENTIALS=/app/svc.json \
  --env LOG_LEVEL=DEBUG \
  us-docker.pkg.dev/your/gar/ppb:development
```

PPB will act as a local ingress proxy to your GCE VM. Visit http://localhost:8080 and PPB will power on your target VM (if needed) then proxy your requests through to the application running on the VM.

The host-network example is for Linux and makes the checked-in loopback
allowlist match the direct browser peer. On Docker Desktop, set `allowedIps` to
the exact host-gateway address reported inside the container instead of adding
a broad private range.

### Google Cloud Run Deployment

Deploy PPB to Cloud Run and configure it as your application's public endpoint:

```bash
gcloud run deploy ppb \
  --image=us-docker.pkg.dev/your/gar/ppb:VERSION@sha256:DIGEST \
  --set-secrets="/app/ppb.yaml=ppb-config:latest" \
  --set-env-vars="PPB_CONFIG_PATH=/app/ppb.yaml" \
  --service-account=your-ppb-service-account@project.iam.gserviceaccount.com \
  --allow-unauthenticated \
  --port=8080 \
  --timeout=600 \
  --max-instances=1 \
  --network=NETWORK \
  --subnet=SUBNET \
  --vpc-egress=private-ranges-only \
  --region=us-central1
```

Keep the Cloud Run request timeout greater than `powerOnTimeout +
dialTimeout`, with enough margin for the proxied application response. The
defaults use a 360-second power budget and 120-second connection window, so the
example configures 600 seconds.

Keep one active PPB instance so concurrent requests share its in-process power
coordinator. Revision overlap can briefly run an old and new instance at once;
PPB reconciles a conflicting start or resume by joining the observed VM
transition. Grant the service account the custom start role on the target
instance, not across the project.

Your users will access `https://ppb-xxx-uc.a.run.app` which automatically powers on your GCE VM and proxies traffic through.
