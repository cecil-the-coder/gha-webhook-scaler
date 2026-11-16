# Burst runners on cloud VMs

Burst runners let jobs overflow from your normal runners (Docker or Kubernetes) onto
short-lived cloud VMs in **your own** cloud account. Each VM is an ephemeral GitHub
Actions runner: it takes exactly one job and is then deleted.

Burst is off unless `BURST_PROVIDER` is set. Supported providers: `gce` (Google Compute
Engine), and `aws` (EC2, see [AWS](#aws)).

## How it works

```
GitHub ── workflow_job queued ──► scaler ──► primary runtime (Docker/K8s)
                                    │            at MAX_RUNNERS? ──► "saturated"
                                    │
                                    └─ burst loop (every BURST_POLL_INTERVAL, or woken)
                                         reads the real queue from GitHub
                                         creates VMs for jobs that waited BURST_AFTER
                                         (at once for saturated owners)

VM boots ──► installs runner if missing ──► POST /v1/runner-config
             (Bearer: cloud instance identity token)
         ◄── {"encoded_jit_config": ...}  (single-use, minted for this VM only)
         ──► run.sh --jitconfig ... ──► one job ──► power off ──► deleted
```

- **Which jobs.** Jobs with status `queued` or `pending` (not `waiting`, which is
  environment approval). A job counts for burst when it has waited longer than
  `BURST_AFTER`, or straight away when the primary runtime reported its owner at
  `MAX_RUNNERS`. `BURST_AFTER=0` turns burst into the only runtime for matching jobs.
- **Which pool.** GitHub's own rule: a runner takes a job only if it has *every* label
  the job asks for (case-insensitive). Burst runners carry `self-hosted`, `Linux`, their
  architecture (`X64` or `ARM64`), the pool's `LABELS` and the owner's `OWNER_<X>_LABELS`.
  So `runs-on: [self-hosted, arm64]` only starts an arm64 VM, and `runs-on: ubuntu-latest`
  never starts one.
- **Which repos are read.** Single-repo owners are always read. For `ALL_REPOS` owners,
  a repo is read for `BURST_ACTIVE_WINDOW` after a `workflow_job` webhook (or a
  saturation signal) for it. That keeps the GitHub API cost bounded.
- **Reaping.** A VM is deleted when its job is done, when it is registered but idle
  with nothing to take for `BURST_IDLE_TIMEOUT`, when it never registers within
  `BURST_BOOT_TIMEOUT`, when it is preempted, or when it is older than
  `BURST_MAX_VM_AGE`. If GitHub cannot be read for a VM, it is never deleted on a guess.
- **Caps.** `BURST_MAX_VMS_TOTAL` across pools, `BURST_POOL_<X>_MAX_VMS` per pool, and a
  daily VM-hour budget `BURST_MAX_VM_HOURS_PER_DAY` (UTC day, saved across restarts).
  After a quota or zone-capacity error, creates pause in every pool for 2 minutes.

### Security model

- **No GitHub credential ever reaches a VM.** VM metadata holds only public values: the
  config URL, runner version, Docker switch and the pool's non-secret runner env.
- A VM proves who it is with a token its cloud signs (GCE: an instance identity token
  whose audience is `BURST_PUBLIC_URL`). The scaler accepts it only for a VM it created
  itself and recorded as pending, and issues **one** JIT config per VM, ever. That rule is
  saved to disk before the config is minted, so a restart cannot issue a second one.
- A JIT config registers a single ephemeral runner with fixed labels in a fixed runner
  group. It expires in about an hour.
- The config is in `run.sh`'s argv while the runner lives, because `run.sh --jitconfig`
  is the only way to use one. This is acceptable: the VM is single-tenant and the config
  is single-use.
- The endpoint does not trust network location. Behind a load balancer or tunnel the
  peer address is meaningless, so the identity token is the control.

## Requirements

- The scaler must be reachable from the VMs over **HTTPS with a publicly trusted
  certificate** at `BURST_PUBLIC_URL`. This is usually the same ingress or tunnel that
  serves `/webhook`. Route `POST /v1/runner-config` to the scaler, and don't put an
  interactive login (SSO proxy) in front of that path.
- **Run exactly one replica** of the scaler with burst on. Two replicas would each create
  VMs for the same jobs. Disable any HorizontalPodAutoscaler.
- **Persistent state.** `BURST_STATE_DIR` (default `/var/lib/gha-webhook-scaler/burst`)
  holds `issued.json` (the one-config-per-VM record) and `budget.json`. Mount a volume
  there. Losing it does not leak anything, but VMs created before the loss can no longer
  get a config and are reaped at `BURST_BOOT_TIMEOUT`.
- **GitHub token.** Besides what the scaler already needs, the owner's token must be able
  to manage self-hosted runners:
  - organizations: fine-grained "Self-hosted runners: read and write" (org);
  - user accounts: "Administration: read and write" on the repos.
  It must also read Actions runs ("Actions: read").
- **Organizations: runner group.** JIT runners join `OWNER_<X>_RUNNER_GROUP_ID` (default
  `1`, the Default group). The group must allow the repositories whose jobs should burst,
  or VMs start and sit idle until reaped.

## Google Compute Engine (`BURST_PROVIDER=gce`)

### One-time GCP setup

```sh
PROJECT=my-ci-project
gcloud services enable compute.googleapis.com --project "$PROJECT"

# The identity the VMs run as. It needs no roles; logging.logWriter is optional.
gcloud iam service-accounts create burst-runner-vm --project "$PROJECT"

# The identity the scaler runs as
gcloud iam service-accounts create gha-scaler --project "$PROJECT"
gcloud projects add-iam-policy-binding "$PROJECT" \
  --member "serviceAccount:gha-scaler@$PROJECT.iam.gserviceaccount.com" \
  --role roles/compute.instanceAdmin.v1
gcloud iam service-accounts add-iam-policy-binding \
  "burst-runner-vm@$PROJECT.iam.gserviceaccount.com" --project "$PROJECT" \
  --member "serviceAccount:gha-scaler@$PROJECT.iam.gserviceaccount.com" \
  --role roles/iam.serviceAccountUser
```

The scaler authenticates with **Application Default Credentials**: Workload Identity on
GKE, the metadata server when it runs on GCE, or a key file named by
`GOOGLE_APPLICATION_CREDENTIALS`.

**Quota.** New projects often have a global `CPUS_ALL_REGIONS` quota of 12 vCPUs, which
is three 4-vCPU VMs. Set `BURST_MAX_VMS_TOTAL` to what your quota allows. Otherwise
creates fail with `QUOTA_EXCEEDED` and pause for 2 minutes each time.

### Example

```sh
BURST_PROVIDER=gce
BURST_PUBLIC_URL=https://scaler.example.com          # /v1/runner-config is appended
BURST_GCE_PROJECT=my-ci-project
BURST_GCE_ZONE=us-central1-a
BURST_GCE_VM_SERVICE_ACCOUNT=burst-runner-vm@my-ci-project.iam.gserviceaccount.com
BURST_POOLS=x64,arm
BURST_POOL_X64_MACHINE_TYPE=e2-standard-4
BURST_POOL_ARM_MACHINE_TYPE=t2a-standard-4           # Arm: arch detected from t2a-/c4a-/n4a-
BURST_MAX_VMS_TOTAL=3
OWNER_MYORG_RUNNER_GROUP_ID=1
```

### VM images

The default is Canonical's Ubuntu 24.04 image family for the pool's architecture. On a
stock image the bootstrap:

- installs Docker (`docker.io`, plus `docker-buildx` when available);
- downloads the GitHub runner for `BURST_RUNNER_VERSION`, checking its sha256 against
  the release's published digest;

and adds about 1-2 minutes to boot. For faster starts, **bake an image**:

- the runner unpacked in `/opt/actions-runner` (`run.sh` present, `bin/installdependencies.sh` run);
- a `runner` user that owns it, in the `docker` group if you use Docker;
- `python3`, `curl` and `setpriv` (util-linux), which every Ubuntu image has.

Set `BURST_POOL_<X>_IMAGE` to the image or family (`my-runner-image`,
`projects/my-ci-project/global/images/family/my-runner`). The bootstrap skips the install
when `/opt/actions-runner/run.sh` exists.

If your image needs job environment variables (for example a pre-created buildx
builder), use `BURST_POOL_<X>_RUNNER_ENV="BUILDX_BUILDER=mybuilder;FOO=bar"`. JIT runners
skip `config.sh`, so they don't get a `.env` any other way. These values are public
instance metadata: names that look like credentials are refused.

### Debugging a VM

The bootstrap logs steps and HTTP status codes (never tokens or configs) to the serial
console:

```sh
gcloud compute instances get-serial-port-output gha-burst-x64-0a1b2c3d --zone us-central1-a
```

The scaler logs one structured line per decision (`decision`, `create`, `delete`,
`runner_config`, `create_backoff`, ...), and `/metrics` has a `burst` section.

## AWS

`BURST_PROVIDER=aws` runs burst VMs on Amazon EC2. Everything outside this section
(job selection, pools, caps, reaping, the one-config-per-VM rule) works the same as for
any provider.

### What a VM looks like

- One-time **spot** instance (`BURST_AWS_SPOT=false` for on-demand), interruption
  behaviour `terminate`.
- `InstanceInitiatedShutdownBehavior=terminate`: when the runner exits, the bootstrap
  powers off and the instance is gone. The scaler also terminates VMs by the usual rules.
- **IMDSv2 required**, hop limit 1 (containers on a Docker bridge network can't get an
  IMDS session token).
- Tags: `Name=<vm name>`, `managed-by=gha-webhook-scaler`, `gha-scaler-pool=<pool>`, on
  the instance and its volume. The scaler only lists and terminates instances with the
  `managed-by` tag.
- The VM name is the `RunInstances` client token, so a retried create never starts a
  second instance.
- User-data is the gzipped bootstrap script plus **public** settings only: the config
  URL, runner version, Docker switch and the pool's runner env. No GitHub token, AWS key
  or JIT config is ever put in user-data or tags.
- Subnets in `BURST_AWS_SUBNET_IDS` are used in turn. A capacity error that is specific to
  one Availability Zone (`InsufficientInstanceCapacity`, `SpotMaxPriceTooLow`, ...) moves
  on to the next subnet. Account-wide limits (`VcpuLimitExceeded`,
  `MaxSpotInstanceCountExceeded`, `InstanceLimitExceeded`) and capacity errors in every
  subnet pause creates in every pool for 2 minutes.

### How a VM proves who it is

EC2 has no audience-bound identity token like GCE's, so the VM uses the pattern of
Vault's AWS IAM auth method:

1. The bootstrap gets an IMDSv2 session token (`PUT /latest/api/token`), then the
   temporary credentials of its instance role.
2. With them it **signs, but does not send**, an `sts:GetCallerIdentity` request to
   `https://sts.<region>.amazonaws.com/` (SigV4 in Python's standard library; no AWS
   CLI). The signed headers include `X-Gha-Scaler-Audience: <BURST_PUBLIC_URL>`.
3. It sends the signed request (method, URL, headers, body, as base64 JSON) once, as the
   bearer token of `POST /v1/runner-config`. The credentials never touch disk or argv.

The scaler then:

- accepts only `POST` to exactly `sts.amazonaws.com` or `sts.<BURST_AWS_REGION>.amazonaws.com`
  over https, with no port, path, query or redirect (so the token can't point the scaler
  anywhere else), and a body of exactly `Action=GetCallerIdentity&Version=2011-06-15`;
- requires `host`, `x-amz-date` and `x-gha-scaler-audience` among the signed headers, the
  audience equal to `BURST_PUBLIC_URL`, and a signing time within the last 5 minutes;
- sends the request to STS, which checks the signature and answers with the caller's
  ARN. It must be `arn:aws:sts::<account>:assumed-role/<BURST_AWS_INSTANCE_ROLE>/<instance-id>`,
  the session EC2 opens for an instance profile;
- looks the instance up: same account, `managed-by=gha-webhook-scaler`, pending or
  running, carrying `BURST_AWS_INSTANCE_PROFILE`. Its `Name` tag is the VM's identity.

Rejections are logged with the failed check only, never the token.

**Threat notes.**

- The proof is only as strong as the instance role's **trust policy**. It must trust
  `ec2.amazonaws.com` and nothing else. If a user or another role could
  `sts:AssumeRole` into it with a session name like `i-0123...`, they could pose as that
  instance.
- Any process on the VM, including the job, can reach IMDS and sign such a request. That
  gains nothing: the VM's one config is issued before the job starts, and a second request
  gets 409.
- A captured token can be replayed for up to 5 minutes, but only to this scaler (the
  audience is signed) and only for the VM that signed it, which already has its config.
  Use TLS end to end. Other services that accept presigned `GetCallerIdentity` requests
  (for example Vault with a required server-ID header) won't accept a token signed for
  this audience, and vice versa.
- The instance role has **no permissions**, so leaked instance credentials only allow
  signing more identity proofs for that same VM.

### One-time AWS setup

Replace `123456789012` with your account ID.

**1. The instance role and profile the VMs run as.** It needs no permission policies.

```sh
cat > trust.json <<'EOF'
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Service": "ec2.amazonaws.com" },
    "Action": "sts:AssumeRole"
  }]
}
EOF
aws iam create-role --role-name gha-burst-runner --assume-role-policy-document file://trust.json
aws iam create-instance-profile --instance-profile-name gha-burst-runner
aws iam add-role-to-instance-profile --instance-profile-name gha-burst-runner --role-name gha-burst-runner
```

**2. The scaler's own permissions.** Attach this policy to whatever identity the scaler
runs as (an IAM role via IRSA/EKS Pod Identity, an ECS task role, an EC2 instance role,
or keys in the environment; the SDK's default credential chain finds it):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "Launch",
      "Effect": "Allow",
      "Action": "ec2:RunInstances",
      "Resource": "*"
    },
    {
      "Sid": "TagOnLaunchOnly",
      "Effect": "Allow",
      "Action": "ec2:CreateTags",
      "Resource": [
        "arn:aws:ec2:*:123456789012:instance/*",
        "arn:aws:ec2:*:123456789012:volume/*"
      ],
      "Condition": { "StringEquals": { "ec2:CreateAction": "RunInstances" } }
    },
    {
      "Sid": "TerminateOwnVMs",
      "Effect": "Allow",
      "Action": "ec2:TerminateInstances",
      "Resource": "arn:aws:ec2:*:123456789012:instance/*",
      "Condition": { "StringEquals": { "aws:ResourceTag/managed-by": "gha-webhook-scaler" } }
    },
    {
      "Sid": "Read",
      "Effect": "Allow",
      "Action": ["ec2:DescribeInstances", "ec2:DescribeImages"],
      "Resource": "*"
    },
    {
      "Sid": "PassRunnerRole",
      "Effect": "Allow",
      "Action": "iam:PassRole",
      "Resource": "arn:aws:iam::123456789012:role/gha-burst-runner",
      "Condition": { "StringEquals": { "iam:PassedToService": "ec2.amazonaws.com" } }
    },
    {
      "Sid": "ResolveUbuntuAMI",
      "Effect": "Allow",
      "Action": "ssm:GetParameter",
      "Resource": "arn:aws:ssm:*::parameter/aws/service/canonical/*"
    }
  ]
}
```

`ssm:GetParameter` is only needed when a pool's image is an SSM parameter path (the
default). Verifying a VM needs no extra permission: the STS call carries the VM's
signature, not the scaler's. If EBS default encryption uses a customer-managed KMS key,
the scaler also needs the usual EBS key permissions on it (`kms:CreateGrant`,
`kms:GenerateDataKeyWithoutPlaintext`, `kms:Decrypt`, `kms:DescribeKey`).
You can narrow `ec2:RunInstances` to your subnets, security groups, AMIs and instance
types with resource ARNs if you want.

**3. Spot.** An account that has never launched spot instances needs the spot
service-linked role once: `aws iam create-service-linked-role --aws-service-name spot.amazonaws.com`.
Check the "All Standard Spot Instance Requests" vCPU quota as well; it is often small on
new accounts. Set `BURST_MAX_VMS_TOTAL` to what it allows.

**4. Network.** VMs need outbound HTTPS to `BURST_PUBLIC_URL`, to GitHub
(`github.com`, `api.github.com`, `*.actions.githubusercontent.com`, release downloads)
and, on stock images, to the Ubuntu archive. Use a public subnet with
`BURST_AWS_ASSOCIATE_PUBLIC_IP=true` or a private subnet with a NAT gateway. VMs don't
need inbound access, so a security group with no inbound rules is fine. They don't call
STS; the **scaler** must be able to reach `sts.<region>.amazonaws.com`.

### Example

```sh
BURST_PROVIDER=aws
BURST_PUBLIC_URL=https://scaler.example.com            # /v1/runner-config is appended
BURST_AWS_REGION=us-east-1
BURST_AWS_SUBNET_IDS=subnet-0123456789abcdef0,subnet-0fedcba9876543210
BURST_AWS_SECURITY_GROUP_IDS=sg-0123456789abcdef0
BURST_AWS_INSTANCE_PROFILE=gha-burst-runner
BURST_POOLS=x64,arm
BURST_POOL_X64_MACHINE_TYPE=m7i.xlarge
BURST_POOL_ARM_MACHINE_TYPE=c7g.xlarge                 # Graviton: arch detected from the "g"
BURST_MAX_VMS_TOTAL=3
```

### AWS settings

| Variable | Default | Meaning |
|---|---|---|
| `BURST_AWS_REGION` | `AWS_REGION`, then `AWS_DEFAULT_REGION` | Region VMs run in; also the regional STS host accepted in identity proofs |
| `BURST_AWS_SUBNET_IDS` | (required) | Comma-separated subnet IDs, tried in turn |
| `BURST_AWS_SECURITY_GROUP_IDS` | VPC default group | Comma-separated security group IDs |
| `BURST_AWS_INSTANCE_PROFILE` | (required) | Instance profile name or ARN the VMs run as |
| `BURST_AWS_INSTANCE_ROLE` | the profile's name | Role name inside the profile, as it appears in the VM's assumed-role ARN. Set it when the role and profile names differ. |
| `BURST_AWS_SPOT` | `true` | One-time spot instances |
| `BURST_AWS_ASSOCIATE_PUBLIC_IP` | subnet default | `true`/`false` to force a public IPv4 address on or off |
| `BURST_AWS_VM_PREFIX` | `gha-burst` | VM names are `<prefix>-<pool>-<8 hex>`. Use a different prefix per scaler sharing an account and region. |

Pool settings mean the same as for other providers, with these differences:

- `BURST_POOL_<X>_MACHINE_TYPE` is an EC2 instance type; the default is `m7i.xlarge`.
  The architecture is `arm64` for Graviton types (a `g` after the generation number:
  `t4g`, `m7g`, `c7gn`, `r8g`, ...; also `a1`), else `x64`.
- `BURST_POOL_<X>_IMAGE` is an AMI ID (`ami-...`) or an SSM parameter path (`/...`,
  optionally `resolve:ssm:/...`). The default is Canonical's Ubuntu 24.04 parameter for
  the pool's architecture,
  `/aws/service/canonical/ubuntu/server/24.04/stable/current/<amd64|arm64>/hvm/ebs-gp3/ami-id`,
  re-read at most hourly. A baked AMI needs what a baked GCE image needs (runner in
  `/opt/actions-runner`, a `runner` user, `python3`, `curl`, `setpriv`) plus cloud-init.
- `BURST_POOL_<X>_DISK_GB` sizes the root gp3 volume, which is deleted with the instance.

### Debugging an EC2 VM

The bootstrap logs steps and HTTP status codes (never credentials, tokens or configs) to
the console:

```sh
aws ec2 get-console-output --instance-id i-0123456789abcdef0 --latest --output text
aws ec2 describe-instances --filters Name=tag:managed-by,Values=gha-webhook-scaler \
  --query 'Reservations[].Instances[].[Tags[?Key==`Name`]|[0].Value,InstanceId,State.Name]'
```

A `runner_config` line with `identity_rejected` has an `identity_rejected_reason` line
before it naming the failed check (for example `caller role` when
`BURST_AWS_INSTANCE_ROLE` doesn't match the profile's role).

## Configuration reference

| Variable | Default | Meaning |
|---|---|---|
| `BURST_PROVIDER` | (off) | `gce` or `aws` |
| `BURST_PUBLIC_URL` | (required) | https URL the VMs call. A bare host gets `/v1/runner-config` appended. Also the identity-token audience. |
| `BURST_AFTER` | `60s` | How long a job waits before burst takes it (skipped for saturated owners) |
| `BURST_POLL_INTERVAL` | `30s` | Reconcile interval (webhooks wake it sooner, at most once per 3 s) |
| `BURST_ACTIVE_WINDOW` | `30m` | How long an `ALL_REPOS` repo is read after activity |
| `BURST_IDLE_TIMEOUT` | `10m` | Delete a registered, idle VM with nothing to take |
| `BURST_BOOT_TIMEOUT` | `15m` | Delete a VM that never registered |
| `BURST_MAX_VM_AGE` | `90m` | Delete any VM older than this. Keep it above your longest job. |
| `BURST_MAX_VMS_TOTAL` | `3` | Cap across pools |
| `BURST_MAX_VM_HOURS_PER_DAY` | `8` | Daily VM-hour budget, `0` = unlimited |
| `BURST_STATE_DIR` | `/var/lib/gha-webhook-scaler/burst` | Persistent state |
| `BURST_DRY_RUN` | `false` | Decide and log only; create, delete and mint nothing |
| `BURST_RUNNER_VERSION` | `latest` | Runner installed on stock images |
| `BURST_INSTALL_DOCKER` | `true` | Install Docker on stock images that lack it |
| `BURST_POOLS` | `x64` | Pool names (lowercase letters/digits) |
| `BURST_POOL_<X>_MACHINE_TYPE` | `e2-standard-4` (GCE), `m7i.xlarge` (AWS) | Machine/instance type |
| `BURST_POOL_<X>_IMAGE` | Ubuntu 24.04 for the arch | Boot image |
| `BURST_POOL_<X>_ARCH` | from machine type | `x64` or `arm64` |
| `BURST_POOL_<X>_LABELS` | | Extra runner labels, comma-separated |
| `BURST_POOL_<X>_MAX_VMS` | `BURST_MAX_VMS_TOTAL` | Cap for this pool |
| `BURST_POOL_<X>_DISK_GB` | `50` | Boot disk size |
| `BURST_POOL_<X>_RUNNER_ENV` | | `NAME=VALUE;...` written to the runner's `.env` (public; no secrets) |
| `BURST_GCE_PROJECT`, `BURST_GCE_ZONE` | (required for gce) | Where VMs are created |
| `BURST_GCE_NETWORK` / `BURST_GCE_SUBNETWORK` | `default` / | VPC |
| `BURST_GCE_VM_SERVICE_ACCOUNT` | project default SA | Identity the VMs run as |
| `BURST_GCE_SPOT` | `true` | Spot VMs (deleted on preemption) |
| `BURST_GCE_EXTERNAL_IP` | `true` | Ephemeral public IP (needed without Cloud NAT) |
| `BURST_GCE_VM_PREFIX` | `gha-burst` | VM names are `<prefix>-<pool>-<8 hex>`. Use a different prefix per scaler sharing a project. |
| `OWNER_<X>_BURST` | `true` | Let this owner's jobs burst |
| `OWNER_<X>_RUNNER_GROUP_ID` | `1` | Org runner group for burst runners |

## Known limits

- **Overlap with the primary runtime.** The primary runtime may start a runner for a job
  burst already covered, or the reverse. GitHub gives the job to whichever registers
  first; the other runner idles and is reaped (at most `BURST_IDLE_TIMEOUT` of VM time).
  A job with no architecture label fits every pool and can start one VM per pool, with the
  same outcome.
- **Org runner groups are not read.** Burst assumes the configured group allows the repo.
- **Bounded queue reads.** Per repo and tick, the 10 most recent runs in each of
  `queued`, `pending` and `in_progress` are read. Jobs in older runs are not counted.
- **In-memory only:** the capacity backoff, and creates whose result was not confirmed
  within 30 s. A restart forgets both. Unconfirmed VMs are still settled by the listing
  and `BURST_BOOT_TIMEOUT`.
- **Shutdown** stops the loop but leaves burst VMs running their jobs. The next start
  reaps them by the usual rules.
