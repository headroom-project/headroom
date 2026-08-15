# headroom

[![ci](https://github.com/headroom-project/headroom/actions/workflows/ci.yml/badge.svg)](https://github.com/headroom-project/headroom/actions/workflows/ci.yml)
[![security](https://github.com/headroom-project/headroom/actions/workflows/security.yml/badge.svg)](https://github.com/headroom-project/headroom/actions/workflows/security.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/headroom-project/headroom/badge)](https://scorecard.dev/viewer/?uri=github.com/headroom-project/headroom)
[![Go Reference](https://pkg.go.dev/badge/github.com/headroom-project/headroom.svg)](https://pkg.go.dev/github.com/headroom-project/headroom)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue)](LICENSE)

**What does this Terraform hit before it breaks?**

Infracost tells you the price. Spacelift tells you the run passed. Nobody tells you the ceiling.

```
CRITICAL  [R1] Scale asymmetry: application outgrows the database
  At full scale the workloads in front of aws_db_instance.main open ~800
  connections against a ceiling of ~450. Saturation lands at 56% of the scale
  this plan already authorises.

    - aws_ecs_service.api scales to 40 tasks (max_capacity of
      aws_appautoscaling_target.api) x 20 connections per task (DB_POOL_SIZE=20
      in aws_ecs_task_definition.api) = 800 connections
    - aws_db_instance.main (db.t3.medium, postgres) accepts ~450 connections by
      default [LEAST(DBInstanceClassMemory/9531392, 5000)]
```

Every rule is the same statement wearing different clothes: **scale asymmetry**, where the consumer scales and the provider does not. That is what generated Terraform gets wrong, because a model has no idea what the traffic looks like, so it takes the default, and the default is the bottleneck.

## Install

```bash
curl -fsSL https://headroomcli.com/install.sh | sh
```

The script downloads the release archive **and** the published checksums, verifies one
against the other, and only then installs. A pipe into a shell asks you to trust the
source, so the least it can do is not trust the download. Read it first if you like:
it is [`install.sh`](install.sh) in this repository.

If you would rather not pipe anything into a shell:

```bash
go install github.com/headroom-project/headroom/cmd/headroom@latest

# or download and verify by hand
curl -LO https://github.com/headroom-project/headroom/releases/latest/download/headroom_linux_amd64.tar.gz
curl -LO https://github.com/headroom-project/headroom/releases/latest/download/checksums.txt
sha256sum -c checksums.txt --ignore-missing
```

Every release also ships a Sigstore signature over `checksums.txt`, build provenance,
and an SPDX SBOM per archive. [`SECURITY.md`](SECURITY.md) has the verification commands.

## Usage

```bash
terraform plan -out=tfplan
terraform show -json tfplan > plan.json
headroom analyze plan.json
```

Exit codes, because this is meant to run in a pipeline:

| Code | Meaning |
|---|---|
| 0 | ran, and nothing matched `--fail-on` |
| 1 | ran, and a finding matched `--fail-on` |
| 2 | could not run |

Without `--fail-on` there is no gate at all, whatever the findings say. A capacity tool
must never be the reason a deploy is stuck.

| Flag | Purpose |
|---|---|
| `--json` | findings as JSON |
| `--dry-run` | print the exact redacted payload that would be uploaded, upload nothing |
| `--pool-size N` | connections per task to assume when the task definition does not declare one (default 10) |
| `--warn-at R` | utilization ratio that triggers a warning (default 0.8) |
| `--salt S` | per-organization salt for hashing addresses (env `HEADROOM_SALT`) |
| `--fail-on SEV` | exit 1 on `critical`, or on `warning` and worse |

## Your plan file never leaves your machine

The CLI parses locally and reads by **allowlist, never denylist**. Terraform plan JSON can contain sensitive values, so instead of trying to strip secrets out (which leaks the first time a provider adds an attribute nobody anticipated), only the handful of attributes that matter for capacity is ever read. Everything else is never touched, so it cannot escape.

- Resource addresses are hashed with a per-organization salt. Real names exist only on your machine, and the local report re-attaches them.
- Finding text is not uploaded either: only a rule id, a severity and the bare numbers travel, and the sentence is rebuilt server-side.
- `container_definitions` is deliberately excluded. The connection pool size is derived from it locally and only the resulting integer leaves.
- `headroom analyze --dry-run` prints the exact payload. There is no second channel.

Said plainly, because a privacy claim that overstates itself is worse than one that does not: the allowlisted attributes travel **with their real values**. A subnet CIDR, an instance class and an allocated storage size are in the payload as themselves, because the ceiling cannot be recomputed without them. What is hashed is identity, not shape. Run `--dry-run` and you are looking at all of it.

One dependency, `gopkg.in/yaml.v3`, which has no dependencies of its own. Everything else is the standard library, because this runs inside customer environments and every transitive dependency is supply-chain surface someone has to defend during a security review. The CLI makes no network calls at all unless you ask it to upload.

## Configuration

Drop a `headroom.yaml` at the root of a repository and it is found automatically, next to the plan first and then in the working directory. `--config` points at one explicitly, `--no-config` ignores it. See `headroom.example.yaml`.

Four layers, in increasing order of how much rope they hand you:

| Layer | What it does |
|---|---|
| `defaults` / `rules` | tune the built-in capacity rules: enable, disable, reseverity, change a threshold |
| `catalog` | state facts about this account that no plan can state |
| `exceptions` | silence a finding, with a mandatory reason and end date |
| `custom` | assertions this organization enforces |

**The built-in capacity rules are not authorable in YAML, on purpose.** Their value is the curated ceiling behind them, and a ceiling nobody verified is worse than no ceiling at all. What YAML gets is policy: `gp2 is banned`, `subnets must be at least a /24`, `every database must declare storage autoscaling`.

The `catalog` block is worth more than it looks. A line like

```yaml
catalog:
  rds_max_connections:
    aws_db_instance.reporting: 2000
```

replaces an AWS API call with a human who already knows the answer. The finding stops carrying lowered confidence, and nobody had to grant anybody a role to get there.

Exceptions require both a reason and an expiry. A suppression with no end date outlives the reason for it, so on the day it expires the finding comes back carrying the reason that was given at the time.

Custom rule operators: `equals`, `not_equals`, `in`, `not_in`, `exists`, `matches`, `not_matches`, `gt`, `gte`, `lt`, `lte`, and `cidr_prefix_gt` / `gte` / `lt` / `lte`. Attribute paths reach one nested block deep, as in `root_block_device.volume_type`. There is no expression language: a config file that can compute anything is a program, and a program in a config file is a debugging problem shipped to the customer.

## Rules

| # | Rule | Status |
|---|---|---|
| R1 | Database connections: `max_tasks x pool_size` vs `max_connections(instance_class, engine)` | ✅ |
| R2 | Subnet address exhaustion: awsvpc tasks vs usable IPs in the CIDR | ✅ |
| R3 | SQS and Lambda: visibility vs timeout, poller starvation, and the drain rate a concurrency cap implies | ✅ |
| R4 | Egress concentration: subnets, zones and workloads behind one NAT gateway | ✅ |
| R5 | EBS: gp2 burst credit cliff, and gp3 ratios AWS refuses | ✅ |
| R6 | Asymmetric autoscaling: consumer scales, provider is fixed, and storage that cannot grow | ✅ |
| R7 | Burstable CPU: baseline vCPUs, and whether credits throttle or bill | ✅ |
| R8 | VPN connections on a virtual private gateway do not aggregate | ✅ |

### Azure

| # | Rule |
|---|---|
| AZ1 | Flexible Server connections vs the app tier, reporting `max_user_connections` rather than the headline number |
| AZ2 | AKS node subnet exhaustion: Azure CNI reserves one address per pod, up front, per node |
| AZ3 | NAT gateway SNAT port exhaustion, with pods as the divisor |
| AZ4 | VPN gateway SKU throughput is shared, and connections do not add to it |
| AZ5 | Managed disk tier vs what the VM size can actually drive |
| AZ6 | B-series credits, which throttle rather than bill: Azure has no unlimited mode |

### Google Cloud

| # | Rule |
|---|---|
| GC1 | Cloud SQL connections, using the `database_flags` override when the plan declares one |
| GC2 | GKE addresses: pod secondary range, node primary range, and services range |
| GC3 | Cloud NAT port allocation against the VMs behind the gateway |
| GC4 | Persistent disk performance derived from size |
| GC5 | Serverless VPC connector throughput against what sits behind it |
| GC6 | Cloud SQL storage frozen or capped while the tier in front of it scales |

A rule that cannot ground its numbers stays silent and says what it skipped. A wrong number in a capacity report costs the whole customer, so an absent ceiling always beats an uncertain one.

R4 never reports critical. A single NAT gateway is usually a deliberate cost decision, and the job here is to make the trade visible, not to overrule it.

## Known limits in v0

**Instance values collapse onto the block.** Edges are declared once per resource block, but `for_each` turns one block into many real resources. The graph is keyed by the base address and keeps the first instance's attributes, while instance counts come from `Instances()`. R4 counts instances correctly; R2 attributes demand to the block rather than to each subnet, so a `for_each` over subnets with different CIDRs is approximated. Fix is to key capacity attributes per instance.

**AWS, Azure and Google Cloud**, and within each only the resource types in the allowlists under `internal/extract/`. Anything else is invisible rather than wrong. Provider coverage is uneven by design: each cloud ships the rules whose ceilings are actually published, and a rule with no source behind it does not ship. `docs/azure.md` and `docs/gcp.md` list what each one deliberately does not cover and why.

**Pool size is an assumption unless declared.** It is read from the task definition environment when present (`DB_POOL_SIZE` and friends); otherwise `--pool-size` applies and the finding drops to medium confidence.

## The catalog is the product

`internal/catalog/data/` holds the ceiling tables: given a resource in a given configuration, what is its real limit. That number is almost never in the Terraform. `db.t3.medium` does not say "450 connections" anywhere in the state; it comes from a parameter group formula over the instance class memory, which differs between MySQL and PostgreSQL.

Parsing a plan file is a weekend. The catalog is the part that takes years, which is why it is plain JSON: the knowledge has to survive any rewrite of the code around it.

Every entry carries `source`, `verified_at` and `confidence`. No entry ships without them.

## Security and supply chain

One dependency, `gopkg.in/yaml.v3`, which has none of its own. Everything else is the
standard library. That is a constraint, not an accident of scope: this binary runs
inside customer environments, and every transitive dependency is supply-chain surface
somebody has to defend during a security review.

Scanned on every push, every pull request, and weekly on a schedule:

| Check | What it covers |
|---|---|
| `govulncheck` | Go advisories at **symbol level**: an advisory is reported only when a code path actually reaches the vulnerable function, so a dependency-graph match with no reachable call does not become noise |
| CodeQL, `security-extended` | static analysis of this repository's own Go code |
| OpenSSF Scorecard | the repository's own supply-chain posture, published so anybody can check it |
| Dependency review | blocks a pull request that would add a dependency with a known advisory |
| Dependabot | Go modules and GitHub Actions, weekly |

Results are in the [Security tab](https://github.com/headroom-project/headroom/security).
`govulncheck` runs again inside the release workflow before anything is published, so a
release cannot ship past an advisory that CI would have caught.

To report a vulnerability, see [`SECURITY.md`](SECURITY.md). Please do not open a public
issue for one.

## Contributing

[`CONTRIBUTING.md`](CONTRIBUTING.md) has the setup, the pull request flow and what a
change has to carry. Two rules matter more than the rest:

**New behaviour arrives with a test, and a fix arrives with a test that fails without
it.** Two ceilings once shipped wrong through a fully green suite, because the tests
asserted that the code did what the catalog said and said nothing about whether the
catalog was true.

**No catalog entry ships without `source`, `verified_at` and `confidence`,** where the
source is a deep link to the vendor page that states the figure. If a number cannot be
found in official documentation, leave it out and let the rule stay silent.

A finding that quotes a wrong number is the most valuable bug report this project can
receive. There is an [issue template](.github/ISSUE_TEMPLATE/wrong-number.yml) for
exactly that.

## Development

```bash
go build -o bin/headroom ./cmd/headroom
go test ./...
```

### On this Windows machine

Windows Application Control blocks unsigned binaries, which includes anything `go build`, `go run` or `go test` produces. Cross-compile and run through WSL:

```powershell
$env:GOOS='linux'; $env:GOARCH='amd64'
go build -o bin/headroom-linux ./cmd/headroom
go test -c -o bin/rules.test ./internal/rules
$env:GOOS=''; $env:GOARCH=''

wsl -e /mnt/d/Projetos/headroom/bin/headroom-linux analyze /mnt/d/Projetos/headroom/fixtures/01-ecs-rds/plan.json
```

`.\run.ps1 analyze` and `.\run.ps1 test` wrap both. `run.ps1 test` discovers every
package holding a `_test.go` and runs each one, rather than a hardcoded list: a
hardcoded list is how the CLI went a long time with no test running against it at all.

### Fixtures

`fixtures/01-ecs-rds/` is real Terraform that produces a real plan, not a hand-written JSON blob. It plans without an AWS account: the provider gets fake credentials and every validation call is skipped.

```powershell
cd fixtures/01-ecs-rds
terraform init
terraform plan -out=tfplan
terraform show -json tfplan > plan.json
```

The fixture is shaped like Terraform a model would generate from "give me an ECS service on Fargate with a postgres database". Every resource is correct in isolation, the plan applies cleanly, and the sizes never talk to each other.

## License

[Apache License 2.0](LICENSE).

Apache rather than MIT for one reason that matters to the buyer: it grants patent
rights explicitly. A corporate legal review that waves through MIT sometimes stops on
the absence of a patent grant, and this tool is meant to be installed inside companies
that run that review.

The ceiling catalog under `internal/catalog/data/` is compiled from public vendor
documentation. Every entry carries the source URL it was read from and the date it was
verified.
