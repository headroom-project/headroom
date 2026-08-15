# Google Cloud rules

Six rules, ids `GC1`–`GC6`, registered from `internal/rules/gcp.go`. Everything
GCP-specific lives in files matching `gcp*`; nothing outside them was modified.

| # | Rule | Ceiling | Severity ceiling |
|---|---|---|---|
| GC1 | Cloud SQL connections: consumer scale x pool size vs `max_connections(tier memory, engine)` | published memory band | critical |
| GC2 | GKE addresses: pod secondary range vs the /24-per-node blocks, node subnet primary range, services secondary range | derived from the published block table | critical |
| GC3 | Cloud NAT: VMs behind the gateway vs `nat_ips x 64512 / min_ports_per_vm` | published formula | critical |
| GC4 | Persistent disk: sustained IOPS derived from size alone | published per-GiB rates | warning |
| GC5 | Serverless VPC Access connector: aggregate throughput vs the serverless instances behind it | published estimate range | warning |
| GC6 | Cloud SQL storage that cannot grow while the tier in front of it does | n/a, structural | warning |

## Why these six

The brief listed seven candidates. The ratio that decided it was *real pain x
groundable number*, and the second half eliminated more than the first.

**Taken.**

- **GC1, Cloud SQL connections.** The AWS R1 shape, and better grounded than R1
  is: Google publishes an explicit memory-to-connections table, and Cloud SQL
  puts the memory in the tier string (`db-custom-2-7680` is 7680 MB), so there
  is no instance-class lookup table to be wrong about. It is also strictly
  better than R1 in one place: an overriding `max_connections` is a
  `database_flags` entry that *is* in the plan, where an RDS parameter group's
  value is not. When the flag is declared the rule uses it and reports high
  confidence instead of dropping to low.
- **GC2, GKE addresses.** The headline. Three ranges feed one cluster and they
  exhaust at wildly different node counts. The pod range is the one that
  surprises people, because GKE carves an aligned block per node — a /24 at the
  default 110 pods — so a /21 pod range holds **eight** nodes next to a node
  pool that autoscales to thirty. The published table and the published formula
  `M = 31 - ceil(log2(Q))` agree on every row, and `TestGCPPodBlockTableMatchesTheFormula`
  pins that agreement.
- **GC3, Cloud NAT ports.** Same family as AWS R4 but with a real ceiling
  instead of a blast radius, because Google publishes both halves: 64512 ports
  per address, and static allocation that spends `min_ports_per_vm` per VM at
  placement time. It is also the nastiest failure mode in the set: raising
  `min_ports_per_vm` to fix connection failures *inside* one VM divides the
  number of VMs the gateway can serve by the same factor, and the VMs that lose
  look like bad nodes rather than a bad gateway.
- **GC4, persistent disk.** Kept, but narrowed, see the honesty section below.
- **GC5, Serverless VPC connector.** Kept as a warning only. The asymmetry is
  real and invisible — every private packet from a hundred-instance Cloud Run
  service crosses at most ten connector instances — but Google publishes the
  throughput as an estimated *range*, so the rule quotes the low end, states the
  ratio, and refuses to call it broken.
- **GC6, Cloud SQL storage.** Cheap, structural, no catalog number needed. A
  full Cloud SQL disk stops the instance rather than slowing it.

**Left out.**

- **Cloud Run `max_instance_count` in front of a fixed Cloud SQL** (candidate 4)
  is not a separate rule: it *is* GC1's demand side. Splitting it would produce
  two findings about one defect, and the AWS set already shows what that costs
  (R1 and R6 overlap on the same database). What was taken from candidate 4 is
  the part that matters — a Cloud Run service with no `scaling` block is not a
  one-instance service, it is a hundred-instance service that happens to be
  idle, and the catalog encodes that default so a silent plan still produces a
  grounded demand figure.
- **Shared VPC / secondary range sizing** (candidate 5) collapsed into GC2. A
  secondary range only means something in relation to the cluster consuming it,
  and once GC2 exists the standalone version is the same arithmetic without the
  demand side.
- **Persistent disk vs vCPU count** (candidate 3, second half) was dropped. See
  below.

## What could not be verified

This is the part that matters. Everything below was checked against
`cloud.google.com` (now redirecting to `docs.cloud.google.com`) on **2026-08-14**.

### Verified against live documentation

| Number | Source |
|---|---|
| PostgreSQL `max_connections` bands (25/50/100/200/400/500/600/800/1000) | Cloud SQL for PostgreSQL flags page, `max_connections` row |
| `db-f1-micro` = 0.614 GB, `db-g1-small` = 1.7 GB, `db-perf-optimized-N-*` = 8 GB/vCPU | Cloud SQL machine series overview |
| GKE pod block table (8→/28 … 256→/23) and `M = 31 - ceil(log2(Q))` | Flexible pod CIDR page |
| GKE default 110 pods per node, cap 256 | Flexible pod CIDR / VPC-native clusters |
| 4 reserved addresses per subnet primary range; /20 primary quoted as 4092 nodes | VPC-native clusters |
| Services range: /28 = 16 Services, /20 = 4096 Services | VPC-native clusters |
| 64512 ports per NAT address; Public NAT default 64 ports/VM; worked example 1 IP = 1008 VMs | Cloud NAT ports and addresses |
| pd-standard 0.75 read / 1.5 write IOPS per GiB, 0.12 MiB/s per GiB; pd-balanced 6/6 and 0.28 | Compute Engine disk performance |
| Connector throughput ranges (f1-micro 100–500, e2-micro 200–1000, e2-standard-4 3200–16000 Mbps); min 2 / max 10 instances | Serverless VPC Access |
| Cloud Run default max instances = 100 | Cloud Run max instances |

### Could not be verified, and what was done about it

1. **Cloud SQL for MySQL `max_connections`.** No published memory-to-connections
   table exists. The MySQL quotas page explicitly tells you to run
   `SHOW VARIABLES LIKE "max_connections"` on the instance. **Nothing is
   encoded**; GC1 emits an info finding naming what it skipped. Same for SQL
   Server. This means GC1 currently only produces a number for PostgreSQL.
2. **The GB→MB conversion in the PostgreSQL bands.** Google's table is written
   in GB ("from 7.5 to < 15"); the tier string is in MB. The catalog converts at
   1 GB = 1024 MB, which is consistent with `db-custom-2-7680` being documented
   as 7.5 GB, but the conversion is *inference*, not a quoted figure. It only
   matters for tiers sitting within ~2% of a band edge. Marked in
   `gcp-cloudsql.json` notes.
3. **Memory between 1.7 GB and 3.75 GB.** The published table has a hole there.
   The catalog does **not** interpolate; `GCPSQLMaxConnections` returns
   `ok=false` and the rule stays silent. Pinned by
   `TestGCPUndocumentedMemoryBandIsNotInterpolated`.
4. **`db-perf-optimized-N-128`.** Documented as the one member of the family at
   6.75 GB/vCPU rather than 8. The encoded 884736 MB is arithmetic on the
   published ratio, not a quoted number. Flagged as such in the catalog.
5. **Legacy `db-n1-standard-*` / `db-n1-highmem-*` tiers.** The current Compute
   Engine general-purpose page no longer publishes the N1 table, and I was not
   willing to encode 3.75 GB/vCPU from recall. **Not encoded** — GC1 reports
   "connection ceiling not evaluated" for them. This is a real coverage gap:
   older Terraform uses these tiers. Fixing it needs someone to read the N1
   machine type page (or `gcloud sql tiers list`) and add them with a source.
6. **N4 / C4A Enterprise Plus predefined Cloud SQL families.** Not checked at
   all this pass. Not encoded, same skip path.
7. **Persistent disk per-vCPU caps.** This was candidate 3's second half and the
   one thing I could not ground. Google no longer publishes a single
   vCPU-to-IOPS table; the limits are given per machine family (C2, C2D, N2, A2,
   A3, …) with zonal disks topping out at 80000 IOPS. Encoding one table would
   have been inventing precision. **Not encoded**, and GC4 says so in every
   finding. The omission is directionally safe: the size-derived number is an
   upper bound, so the real ceiling can only be lower, and a finding that fires
   on the size-derived number would fire on the true number too.
8. **Private NAT.** The 32-ports default and the x2 reliability multiplier are
   from the same page as the public numbers, but the code path has never seen a
   real private-NAT plan. Marked `medium` in `gcp-nat.json`.
9. **Connector throughput.** Google labels the ranges as estimates, not
   guarantees. Marked `medium`; GC5 quotes only the low end and never goes past
   warning.

### Numbers that are headroom's own, not Google's

Two thresholds are judgement calls. Both are in the catalog next to the numbers
they sit beside, both are stated in the finding text, and neither can produce a
critical.

- **`reporting_floor.read_iops = 100`** (`gcp-disk.json`): the point below which
  a disk's IOPS figure is worth printing.
- **`gcpServicesWarnBelow = 1024`** (`gcp_gke_addresses.go`): a services
  secondary range smaller than a /22 is materially below what GKE would have
  chosen and cannot be changed for the life of the cluster.

## Fixture

`fixtures/gcp-01-gke-cloudrun-sql/` is real Terraform that produces a real plan,
generated with the `hashicorp/google` provider v6.50.0 and Terraform 1.15.8.

**Offline planning works.** The trick is different from the AWS one: the google
provider parses `credentials` as a service account key, so a fake JSON blob
fails at configure time. Passing a fake **OAuth access token** instead skips key
parsing entirely, and a plan that creates resources and uses no data sources
makes no API calls:

```hcl
provider "google" {
  project      = "headroom-fixture"
  region       = "us-central1"
  zone         = "us-central1-a"
  access_token = "mock-access-token"
}
```

```powershell
cd fixtures/gcp-01-gke-cloudrun-sql
terraform init
terraform plan -out=tfplan
terraform show -json tfplan > plan.json
```

The fixture is shaped like Terraform a model would generate from "give me a GKE
cluster and a Cloud Run API on a postgres database behind Cloud NAT". Every
resource is correct in isolation, the plan applies cleanly, and the sizes never
talk to each other. It triggers all six rules: 3 critical, 5 warning.

## Requests for files I do not own

Three things would improve the GCP rules but live outside `gcp*`. They are
requests, not changes.

1. **`extract.pick` drops nested blocks.** On GCP almost every capacity
   attribute is one or two blocks deep: `settings.tier`,
   `template.scaling.max_instance_count`, `autoscaling.max_node_count`,
   `node_config.disk_type`, `secondary_ip_range[].ip_cidr_range`. `pick` copies
   scalars, strings, bools and string lists, so none of those reach the payload
   as node attributes. The rules read them locally and the numbers travel as
   finding metrics, so nothing is lost for the findings themselves, but the
   node graph the backend sees is thinner for GCP than for AWS. A
   path-aware picker (`"settings.tier"`, `"template.scaling.max_instance_count"`)
   with the same allowlist discipline would fix it without weakening the
   guarantee — the allowlist stays an allowlist, it just gains dotted keys.
2. **`Options` has no GCP knobs.** GC5 reuses `ScaleRatioWarn`, which fits well.
   The two headroom-owned thresholds above are consts rather than flags because
   adding fields to `Options` means editing `rules.go`. If they earn a flag,
   `--iops-floor` and `--service-range-warn` are the shapes.
3. **README rule table.** `README.md` says "AWS only". When GC1–GC6 land it is
   AWS and GCP, and the rules table wants six more rows.

## Known limits

- **GC1 only reports for PostgreSQL.** See point 1 above.
- **GKE workloads are not counted as Cloud SQL consumers.** A node pool's node
  count says nothing about how many pods hold connections, and the Deployment
  that would say is not in the Terraform. Counting nodes would be inventing
  demand, so GC1 counts Cloud Run services and managed instance groups only.
- **Regional clusters multiply node counts by zone.** `max_node_count` on a
  `google_container_node_pool` is per zone for a regional cluster, so a regional
  cluster's true node count is up to 3x what GC2 and GC3 report. The rules
  therefore *understate* demand rather than overstate it, which is the safe
  direction, but it is a real gap: resolving it needs the cluster's
  `node_locations`, which is often unknown at plan time.
- **Autopilot clusters are invisible to GC2.** They have no
  `google_container_node_pool`, and GKE picks max pods per node itself from a
  documented 8–256 range, so there is no grounded node count to compare.
- **`google_cloud_run_service` (v1) scale is not resolved.** The v1 ceiling
  lives in the `autoscaling.knative.dev/maxScale` annotation, and annotations
  are customer content the allowlist deliberately avoids. GC5 counts a v1
  service only if `gcpScaleOf` can ground it, which today it cannot.
