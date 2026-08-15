# Catalog verification pass — 2026-08-14

Every number named below was checked against a primary vendor source on
2026-08-14. Blogs, StackOverflow, other tools' source code and recall were not
used and are not admissible here. Where a page states a figure directly the
entry is `high`; where it had to be derived from something else the page states,
it is `medium` and the derivation is written down; where no official page states
it, nothing is encoded and the rule stays silent.

The rule this catalog exists to serve: **an absent ceiling is always better than
an uncertain one.** A single wrong number in a capacity report destroys trust in
every other finding.

## Verdict table

| # | Entry | Old value | New value | Verdict | Source |
|---|---|---|---|---|---|
| 1 | `aws-rds` postgres `max_connections` formula | `LEAST(DBInstanceClassMemory/9531392, 5000)` | unchanged (confidence medium → high) | confirmed | [CHAP_Limits #MaxConnections](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/CHAP_Limits.html#RDS_Limits.MaxConnections) |
| 2 | `aws-rds` postgres divisor | 9531392 | unchanged | confirmed | same |
| 3 | `aws-rds` postgres cap | 5000 | unchanged | confirmed | same |
| 4 | `aws-rds` mysql divisor | 12582880 | unchanged (confidence medium → high) | confirmed | same |
| 5 | `aws-rds` mysql cap | none | unchanged (no cap) | confirmed | same |
| 6 | **`aws-rds` mariadb divisor** | **12582880** | **25165760** | **corrected** | same |
| 7 | **`aws-rds` mariadb cap** | **none** | **12000** | **corrected** | same |
| 8 | `DBInstanceClassMemory` semantics | open question, entry marked medium | full class memory **minus** OS/RDS reserve; derived ceilings are upper bounds | settled | same |
| 9 | `aws-rds` instance class memory table (60 entries) | as encoded | unchanged, all 60 matched | confirmed | [Concepts.DBInstanceClass.Summary](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Concepts.DBInstanceClass.Summary.html) |
| 10 | `db.t3.medium` / `db.m5.large` / `db.r5.large` / `db.r6g.xlarge` | 4 / 8 / 16 / 32 GiB | unchanged | confirmed | same |
| 11 | `aws-ebs` gp2 IOPS per GiB | 3 | unchanged | confirmed | [general-purpose #gp2-performance](https://docs.aws.amazon.com/ebs/latest/userguide/general-purpose.html#gp2-performance) |
| 12 | `aws-ebs` gp2 minimum baseline | 100 | unchanged | confirmed | same |
| 13 | `aws-ebs` gp2 burst | 3000 | unchanged | confirmed | same |
| 14 | `aws-ebs` gp2 burst-irrelevant size | 1000 GiB | unchanged | confirmed | same |
| 15 | `aws-ebs` gp2 max IOPS | 16000 | unchanged (reached at 5,334 GiB) | confirmed | same |
| 16 | `aws-ebs` gp3 included IOPS | 3000 | unchanged | confirmed | [general-purpose #gp3-performance](https://docs.aws.amazon.com/ebs/latest/userguide/general-purpose.html#gp3-performance) |
| 17 | `aws-ebs` gp3 included throughput | 125 MiB/s | unchanged | confirmed | same |
| 18 | **`aws-ebs` gp3 max IOPS** | **16000** | **80000** | **corrected** | same |
| 19 | **`aws-ebs` gp3 max throughput** | **1000 MiB/s** | **2000 MiB/s** | **corrected** | same |
| 20 | `aws-ebs` gp3 IOPS per GiB | 500 | unchanged | confirmed | same |
| 21 | `aws-ebs` gp3 MiB/s per IOPS | 0.25 | unchanged | confirmed | same |
| 22 | `aws-burstable` T3/T3a/T4g vCPUs, baseline %, credits/hour (21 rows) | as encoded | unchanged, all 21 matched | confirmed | [credit table](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/burstable-credits-baseline-concepts.html#burstable-performance-instances-credit-table) |
| 23 | `aws-burstable` default credit mode T2 | `standard` | unchanged | confirmed | [key concepts](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/burstable-credits-baseline-concepts.html#key-concepts) |
| 24 | `aws-burstable` default credit mode T3/T3a/T4g | `unlimited` | unchanged | confirmed | same |
| 25 | `aws-vpn` throughput per tunnel | 1.25 Gbps | unchanged (confidence medium → high) | confirmed | [vpn-limits #bandwidth](https://docs.aws.amazon.com/vpn/latest/s2svpn/vpn-limits.html#vpn-quotas-bandwidth) |
| 26 | `aws-vpn` ECMP on virtual private gateway | not supported | unchanged | confirmed | [multi-VPC whitepaper, VPN](https://docs.aws.amazon.com/whitepapers/latest/building-scalable-secure-multi-vpc-network-infrastructure/vpn.html) |
| 27 | `aws-vpn` ECMP on transit gateway | supported | unchanged (now qualified: requires dynamic routing) | confirmed | [vpn-limits #bandwidth](https://docs.aws.amazon.com/vpn/latest/s2svpn/vpn-limits.html#vpn-quotas-bandwidth) |
| 28 | `aws-vpn` ECMP source link | `s2svpn/vpn-tunnel-ecmp.html` | replaced, old link carries neither statement | corrected (link) | see 26, 27 |
| 29 | `aws-lambda` SQS min reserved concurrency | 5 | unchanged (confidence medium → high) | confirmed | [services-sqs-configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html#events-sqs-eventsource) |
| 30 | `aws-lambda` visibility timeout multiple | 6x | unchanged | confirmed | [services-sqs-configure #queueconfig](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html#events-sqs-queueconfig) |
| 31 | `aws-lambda` SQS default batch size | 10 | unchanged | confirmed | [with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html#example-standard-queue-message-event) |
| 32 | `aws-lambda` account concurrent executions | 1000 | unchanged (new caveat: reduced for new accounts) | confirmed | [Lambda quotas #compute-and-storage](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html#compute-and-storage) |
| 33 | `gcp-nat` private NAT default ports/VM | 32 | unchanged (confidence medium → high) | confirmed | [ports-and-addresses](https://docs.cloud.google.com/nat/docs/ports-and-addresses) |
| 34 | `gcp-nat` private NAT port multiplier | 2 | unchanged (confidence medium → high) | confirmed | same |
| 35 | `gcp-serverless` connector throughput ranges | 100-500 / 200-1000 / 3200-16000 | unchanged (confidence medium → high) | confirmed | [serverless-vpc-access](https://docs.cloud.google.com/vpc/docs/serverless-vpc-access) |
| 36 | `gcp-serverless` connector instance range | 2 to 10 | unchanged | confirmed | same |
| 37 | Cloud SQL **MySQL** `max_connections` | not encoded | still not encoded, gap now settled | unverifiable | [mysql/flags](https://docs.cloud.google.com/sql/docs/mysql/flags), [mysql/quotas](https://docs.cloud.google.com/sql/docs/mysql/quotas) |
| 38 | Cloud SQL PostgreSQL 1.7–3.75 GB band | gap, not interpolated | gap still present, still not interpolated | confirmed | [postgres/flags](https://docs.cloud.google.com/sql/docs/postgres/flags) |
| 39 | Cloud SQL PostgreSQL bands (7 bands + 2 shared-core) | as encoded | unchanged, all matched | confirmed | same |
| 40 | Cloud SQL legacy `db-n1-*` memory | not encoded | still not encoded | unverifiable | [general-purpose-machines](https://docs.cloud.google.com/compute/docs/general-purpose-machines), [machine-resource](https://docs.cloud.google.com/compute/docs/machine-resource) |
| 41 | Persistent disk per-vCPU caps | not encoded | still not encoded, no usable table exists | unverifiable | [disks/performance](https://docs.cloud.google.com/compute/docs/disks/performance) |
| 42 | `gcp-disk` pd-standard / pd-balanced per-GiB rates | as encoded | unchanged | confirmed | same |
| 43 | **Azure VPN `VpnGw2AZ` Gen1 vs Gen2** | 1250 Mbps only | 1000 (Gen1) / 1250 (Gen2), both carried | corrected (coverage) | [about-gateway-skus](https://learn.microsoft.com/en-us/azure/vpn-gateway/about-gateway-skus#gateway-skus-by-tunnel-connection-and-throughput) |
| 44 | **Azure VPN `VpnGw3AZ` Gen1 vs Gen2** | 2500 Mbps only | 1250 (Gen1) / 2500 (Gen2), both carried | corrected (coverage) | same |
| 45 | **Azure VPN `VpnGw2` Gen1 vs Gen2** | 1250 Mbps only | 1000 (Gen1) / 1250 (Gen2), both carried | corrected (coverage) | same |
| 46 | **Azure VPN `VpnGw3` Gen1 vs Gen2** | 2500 Mbps only | 1250 (Gen1) / 2500 (Gen2), both carried | corrected (coverage) | same |
| 47 | Azure VPN remaining SKU figures (tunnels, P2S, VMs, BGP, ZR) | as encoded | unchanged, all matched at Gen2 | confirmed | same |
| 48 | **Azure VPN "generation is invisible in Terraform"** | asserted in catalog and in Go doc comment | **false**: `generation` is a documented argument and is already allowlisted by `extract` | corrected (claim) | [provider docs](https://registry.terraform.io/providers/hashicorp/azurerm/latest/docs/resources/virtual_network_gateway) |
| 49 | Azure VPN source link | `vpn-gateway-about-vpngateways` | `about-gateway-skus` | corrected (link) | see 43 |
| 50 | `azure-network` NAT gateway SNAT ports per IP | 64512 | unchanged | confirmed | [nat-gateway-resource #snat-ports](https://learn.microsoft.com/en-us/azure/nat-gateway/nat-gateway-resource#snat-ports) |
| 51 | `azure-network` NAT gateway max public IPs | 16 | unchanged | confirmed | same |
| 52 | `azure-network` NAT gateway max concurrent connections | 2,000,000 | unchanged | confirmed | same |
| 53 | `azure-network` NAT gateway per-IP same-destination | 50,000 | unchanged | confirmed | same |
| 54 | `azure-network` NAT gateway idle timeout | 4 / 120 min | unchanged | confirmed | same |
| 55 | **NAT Gateway per-instance SNAT port equivalent** | none found | still none, and now known to be structurally absent | unverifiable | same |
| 56 | `azure-network` SNAT stand-in 1024 / 32 | Load Balancer values | unchanged, provenance now stated in the entry | confirmed | [load-balancer-outbound-connections](https://learn.microsoft.com/en-us/azure/load-balancer/load-balancer-outbound-connections#default-port-allocation-table) |
| 57 | `azure-network` LB ports per frontend IP | 64000 | unchanged | confirmed | same |
| 58 | **Azure MySQL `Standard_B2ms` memory** | 8 GiB, flagged as contradicted | **8 GiB, contradiction resolved** | confirmed | [service tiers #burstable](https://learn.microsoft.com/en-us/azure/mysql/flexible-server/concepts-service-tiers-storage#burstable) |
| 59 | Azure MySQL `max_connections` table (~45 SKUs) | as encoded | unchanged, all matched the *Default value* column | confirmed | [server parameters #max_connections](https://learn.microsoft.com/en-us/azure/mysql/flexible-server/concepts-server-parameters#max_connections) |

## The three corrections that change reported numbers

### 1. RDS MariaDB `max_connections` — every MariaDB ceiling was 2x too high

The catalog claimed MariaDB shared the MySQL formula. It does not.

> For MariaDB 10.5 and higher versions, the default is:
> `LEAST({DBInstanceClassMemory/25165760},12000)` — The formula is effectively
> equivalent to MB/25. If the default value calculation results in a value
> greater than 12,000, Amazon RDS sets the limit to 12,000.
> For MariaDB version 10.4: `{DBInstanceClassMemory/12582880}` — The formula is
> effectively equivalent to MB/12.

The old divisor was the MariaDB **10.4** one. Every MariaDB ceiling this catalog
reported was therefore double the truth. A `db.r5.large` was reported at 1365
connections and is really ~682; a `db.t3.medium` was 341 and is ~170.

This is the only correction in the pass that **lowers** a ceiling, which is to
say the only one where headroom was telling customers they had room they did not
have. Nothing in the test suite or the fixtures exercises the `mariadb` engine,
which is exactly why the error survived.

Residual risk: Terraform's `engine = "mariadb"` does not carry the version, and
the rule does not read `engine_version`. A plan pinned to 10.4 is now
under-reported by 2x. That is the safe direction, and it is recorded in the
entry.

### 2. EBS gp3 ceilings — 5x and 2x too low

`max_iops` was 16000 (really 80000) and `max_throughput_mibps` was 1000 (really
2000). These are not invented numbers: they are exactly what the same AWS page
still publishes for **gp3 on AWS Outposts**, and they were the service-wide
limits before AWS raised them. Both old values are preserved in the entry under
`outposts_max_iops` / `outposts_max_throughput_mibps` so the next reader does not
"rediscover" them and revert the fix.

Direction of the error: R5 was rejecting legal plans. A 20,000 IOPS gp3 volume is
valid and was being reported `CRITICAL — asks for a ratio AWS will refuse`. A
false positive rather than a missed ceiling, but a false `CRITICAL` on a plan
that applies cleanly is the fastest way to lose a reader.

### 3. `DBInstanceClassMemory` is net of the reserve — all RDS ceilings are upper bounds

The open question in the notes is settled:

> `DBInstanceClassMemory` is in bytes. ... Because of memory reserved for the
> operating system and RDS management processes, this memory size is smaller than
> the value in gibibytes (GiB) shown in Hardware specifications for DB instance
> classes.

The catalog feeds the formulas **full** class memory, because AWS publishes the
reserve for no class. So every RDS connection ceiling headroom reports is an
upper bound. AWS gives two worked examples of the size of the gap, both MySQL:

| Class | Memory | Naive arithmetic | AWS's stated real value | Reserve |
|---|---|---|---|---|
| `db.m7g.large` | 8 GiB | 683 | "approximately 630" | ~8% |
| `db.t3.micro` | 1 GiB | 85 | "approximately 60" | ~30% |

The reserve is proportionally worst on the small burstable classes people
actually deploy. The formula, divisor and cap are all confirmed correct, so the
familiar `db.t3.medium` postgres figure of **450 is unchanged** — but it should be
read as "at most 450", and a workload that lands near it is already over it. The
entries now say so, and so does the finding text.

## Gaps confirmed as gaps (do not reopen without new evidence)

**Cloud SQL for MySQL `max_connections`.** Checked the MySQL flags page and the
MySQL quotas page. Neither publishes a table; both direct you to
`SHOW VARIABLES LIKE "max_connections"` on the running instance. Cloud SQL for
PostgreSQL *does* publish the table this catalog encodes, so the asymmetry is
Google's. Upstream MySQL is not a substitute: its compiled-in default is a fixed
number rather than memory-derived, and Cloud SQL overrides it, so importing it
would produce a confidently wrong ceiling instead of a missing one. **This gap is
permanent for a plan-file tool** — the value is only knowable at runtime — and
should not be re-researched every few months.

**Cloud SQL legacy `db-n1-*` memory.** Checked the general-purpose machine family
page and the machine families comparison guide. Neither carries an N1
specification table; both mention N1 only as the first-generation series. The
widely repeated 3.75 GB per vCPU is very probably right, and "very probably" is
not a ceiling. Closing this needs the Cloud SQL Admin API `tiers.list`, which is
a credentialed runtime call, not a catalog edit.

**Persistent disk per-vCPU caps.** Google states "The machine type and the number
of vCPUs on the instance determine the per-instance limits" and then publishes
separate tables per machine family. There is no single vCPU-to-IOPS function that
is correct across families. The size-derived number a rule reports remains an
upper bound and the rule must keep saying so.

**Azure NAT Gateway per-instance SNAT ports.** There is no equivalent to Load
Balancer's preallocation table, for a structural reason rather than a
documentation gap: NAT Gateway does not preallocate. "SNAT port inventory is
available on demand to all instances within a subnet attached to the NAT gateway.
No preallocation of SNAT ports per instance is required." The Load Balancer
1024/32 stand-in is kept, and the entry now carries an explicit warning that the
figure comes from a different service, plus the direction the borrowing errs in:
NAT Gateway's dynamic sharing is strictly better than static preallocation, so a
gateway that fails this yardstick is genuinely tight while one that passes is not
thereby proven safe.

## Two claims in the catalog that were simply wrong

**"Terraform never states the VPN gateway generation."** It does.
`azurerm_virtual_network_gateway` has a `generation` argument — "The Generation of
the Virtual Network gateway. Possible values include `Generation1`, `Generation2`
or `None`" — and headroom's own `internal/extract/azure_allowlist.go` already
allowlists it, so the plan file carries the answer today. AZ4 just does not read
it. The catalog now carries both generations per SKU
(`throughput_mbps_gen1` / `throughput_mbps_gen2`) so switching is a rule change
rather than a research change.

**"Taking the higher of the two generations makes the finding conservative."**
It does the opposite. Quoting 2.5 Gbps for what is really a Generation1
`VpnGw3AZ` at 1.25 Gbps overstates headroom by 2x and understates risk, which is
the one direction a capacity report must never err in. The note now states
plainly that the quoted ceiling assumes Generation2.

The scope of the generation problem was also understated: it was described as
affecting `VpnGw2AZ` and `VpnGw3AZ`. It affects `VpnGw2` and `VpnGw3` identically.

## The Azure MySQL `Standard_B2ms` contradiction, resolved

Microsoft's `max_connections` table lists `Standard_B2ms` as 4 GB. Two other
Microsoft tables say 8, and the arithmetic agrees with them:

1. The service tiers Burstable table: `Standard_B2ms | 2 vCores | 8 GiB physical
   | 8.8 GiB total`.
2. The `innodb_buffer_pool_size` table **on the same page as the disputed one**:
   `Standard_B2ms | 2 | 8` GB, with a default buffer pool of 4294967296 bytes
   (4 GiB), the half-of-memory ratio that page applies to every other burstable
   size. At 4 GB of memory, B2ms would be indistinguishable from B2s.
3. The connection column itself tracks memory at a constant ~85.3 per GB across
   all ~45 rows (1 GB → 85, 2 → 171, 4 → 341, 16 → 1365, 32 → 2731, 64 → 5461).
   B2ms's documented 683 is 8 × 85.3, not 4 × 85.3.

**Verdict: 8 GiB. The memory cell in the `max_connections` table is a typo.** The
catalog's existing value of 8 is correct and stays. Impact on reported ceilings:
none, because `max_connections` for B2ms is 683 either way and is quoted as
printed rather than derived from `memory_gib`.

A related trap is now documented in the entry: `concepts-service-tiers-storage`
has a "Max Connections" column reading 341 / 683 / 1365 for B1ms / B2s / B2ms.
That is not a contradiction and must not be used to "fix" this file — it is the
same series shifted one row, because it prints the **maximum** (matching the
`Max value` column of the server parameters table) while this catalog encodes the
**default**.

## Structural change: `notes` versus `verification`

Several `notes` fields are rendered verbatim into findings the customer reads
(`"Catalog note: " + gp3.Notes` and friends). Audit trails were therefore split:

- `notes` — short, operational, written for the engineer reading the report.
- `verification` — verbatim quotes, what changed, why, and confidence movement.
  Not read by any Go struct, so it never reaches a finding.

Anyone adding to this catalog should keep that split. A twenty-line changelog
printed into a `CRITICAL` finding is its own kind of wrong number.

## Test suite

84 passing, 0 failing, before and after. `go vet ./...` clean.

**No test expected value changed.** The `450` ceiling and `56%` break point in
`pipeline_test.go` survive because the PostgreSQL formula, divisor and cap were
all confirmed correct. The gp3 test violates the 500-IOPS-per-GiB ratio, which
was unchanged, so raising `max_iops` from 16000 to 80000 did not move it. No
test or fixture exercises the `mariadb` engine, which is why the one genuinely
wrong ceiling in the AWS catalog was invisible to the suite.

## Follow-ups for a human (not done here, out of scope)

1. **AZ4 should read `generation`.** The data is in the plan and the catalog now
   carries both generations. Until it does, every Generation1 `VpnGw2`,
   `VpnGw3`, `VpnGw2AZ` or `VpnGw3AZ` gets a ceiling up to 2x too generous.
2. **`internal/catalog/gcp.go` discards catalog parse errors** —
   `_ = json.Unmarshal(...)` for all five GCP files. A malformed GCP catalog
   degrades silently to zero values rather than failing loudly. This was not
   theoretical: a missing comma introduced during this pass produced silently
   wrong ceilings and was caught only because two unrelated tests happened to
   assert on Cloud Run's default instance count. The AWS loader (`Load`) and the
   Azure loader both propagate errors, and Azure has `TestAzureCatalogParses`
   guarding it. GCP should match both.
3. **R5's gp2 guard reads `baseline >= 1000`** (`r005_ebs.go`), which silences the
   burst-cliff finding for every volume from ~334 GiB to 1000 GiB even though
   those volumes still have a real cliff (a 400 GiB gp2 volume bursts to 3000 and
   falls to 1200). If the intent was "baseline has caught up with burst", the
   comparison wants `gp2.BurstIOPS`, not a literal. Catalog-adjacent, but it is a
   rule change and was left alone.
4. **RDS MariaDB version.** Consider reading `engine_version` so a 10.4 instance
   is not silently under-reported by 2x, or dropping to `medium` confidence when
   the version is absent.
5. **No fixture covers GCP Private NAT**, which is why that entry sat at `medium`
   for a reason unrelated to its sourcing. The numbers are now confirmed `high`;
   the coverage gap remains.
