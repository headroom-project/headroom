# Azure

Six rules, `AZ1`–`AZ6`. Every one is the same statement as the AWS set wearing a different accent: the consumer scales and the provider does not. What changes on Azure is *where the ceiling is written down*. AWS mostly derives limits from size (a `db.t3.medium` gets a connection count out of a formula over its memory); Azure mostly sells them in fixed tiers and publishes a table. That makes Azure ceilings easier to ground and easier to miss, because nothing in the Terraform hints that a table exists.

## Rules

| # | Rule | Severity | Catalog |
|---|---|---|---|
| AZ1 | Flexible server connections: `max_workers x pool_size` vs `max_connections(sku_name)` | critical / warning | `azure-postgresql.json`, `azure-mysql.json` |
| AZ2 | AKS node subnet exhaustion: `nodes x (1 + max_pods)` vs the subnet prefix | critical / warning | `azure-aks.json`, `azure-network.json` |
| AZ3 | NAT gateway SNAT port exhaustion: `64,512 x public IPs` divided by endpoints | critical / warning | `azure-network.json` |
| AZ4 | VPN gateway: connections share the SKU's aggregate throughput, they do not add to it | warning (critical past the tunnel limit) | `azure-network.json` |
| AZ5 | Managed disk provisions IOPS/throughput the VM size cannot drive | warning | `azure-disks.json`, `azure-vm.json` |
| AZ6 | B-series baseline: what it sustains, and that Azure has no unlimited mode | warning | `azure-vm.json` |

### Why these six

**AZ2 and AZ3 are the reason to build this at all.** Both are invisible, both are effectively irreversible, and both are made brutal by the same fact: on Azure CNI in node subnet mode, a *pod* is a first-class network endpoint. It has its own virtual network address, reserved up front per node rather than allocated when it schedules, and its own SNAT flows. A cluster that reads as "twelve nodes" is 1,320 addresses and 1,320 outbound-connection sources. A `/24` is 251 addresses; a single public IP is 64,512 SNAT ports. Neither number appears anywhere in the Terraform, a subnet prefix cannot be resized once resources sit in it, and `max_pods` cannot be changed on an existing node pool. AZ3 is the better of the two because SNAT exhaustion does not fail loudly: connections fail intermittently, to some destinations and not others, under load, and it looks like the remote service being flaky.

**AZ1 is the Azure twin of R1** and the single clearest finding, because Microsoft publishes `max_connections` per compute size as a flat table. It also carries a detail the AWS rule does not have to think about: the service reserves 15 connections for replication and monitoring, so the ceiling an application actually gets is `max_user_connections`, not `max_connections`. Reporting the larger number would overstate the headroom by 30% on the smallest tier.

**AZ5 is the sharpest thing in Azure that has no AWS analogue.** AWS gp3 lets you buy IOPS directly and rejects impossible ratios at apply time (that is R5). Azure sells disk performance in fixed tiers keyed on size, sells VM disk throughput on a completely separate ladder, and never mentions the two in the same document. A 1 TiB Premium SSD is a P30 at 5,000 IOPS; a `Standard_B2s` drives 1,280 uncached. Nothing fails. The money is spent, the performance never arrives, and the reason is in neither resource.

**AZ6 is deliberately not a copy of R7.** On AWS the T3 default is `unlimited`: the machine keeps performing and bills for it, so the ceiling is an invoice. Azure B-series has no unlimited mode at all — no setting, no surcharge, no escape. When the bank hits zero the VM is throttled to its base percentage until the load stops. The advice inverts, so the rule has to too.

**AZ4 is the direct sibling of R8.** Same shape, better evidence: Microsoft states in one sentence that "all VPN tunnels share the available gateway bandwidth", and the SKU table prints the number, so the finding can quote both.

### Considered and not built

- **Azure SQL Database (`azurerm_mssql_database`) session and worker limits.** The DTU and vCore limit tables are real and published, but they are two large matrices (one per purchasing model, with separate serverless behaviour), and the `sku_name` grammar covers `S3`, `GP_S_Gen5_2`, `BC_Gen5_4` and elastic pool membership. Doing it properly is its own piece of work, and doing it approximately would put a wrong number in a capacity report. The type is already on the extract allowlist so the shape is visible; the rule is the obvious next one.
- **App Service Plan instance limits.** The per-SKU maximum instance counts are documented, but the interesting failure is almost always the database behind the plan, which AZ1 already covers using the plan's scale as its input.
- **Application Gateway v2 capacity units and its dedicated subnet.** A v2 gateway consumes a private address per capacity unit out of a subnet that may hold nothing else. Groundable and worth building; it did not make the first six.
- **Storage account request-rate limits** (20,000 requests/second per account). Real, and a genuine asymmetry when many workloads share one account, but the demand side is not in the plan at all, so the rule would have no number to compare against.

## Catalog sources

Every table was read from Microsoft Learn on 2026-08-14 and every entry carries `source`, `verified_at` and `confidence`.

| File | Source | Confidence |
|---|---|---|
| `azure-postgresql.json` | [Limits in Azure Database for PostgreSQL flexible server](https://learn.microsoft.com/en-us/azure/postgresql/flexible-server/concepts-limits) | high |
| `azure-mysql.json` | [Server parameters in Azure Database for MySQL flexible server](https://learn.microsoft.com/en-us/azure/mysql/flexible-server/concepts-server-parameters) | high |
| `azure-aks.json` | [IP address planning in AKS](https://learn.microsoft.com/en-us/azure/aks/concepts-network-ip-address-planning) | high |
| `azure-network.json` (subnet) | [Azure Virtual Network FAQ](https://learn.microsoft.com/en-us/azure/virtual-network/virtual-networks-faq) | high |
| `azure-network.json` (NAT) | [NAT gateway resource](https://learn.microsoft.com/en-us/azure/nat-gateway/nat-gateway-resource) | high |
| `azure-network.json` (LB SNAT yardstick) | [SNAT for outbound connections](https://learn.microsoft.com/en-us/azure/load-balancer/load-balancer-outbound-connections) | high |
| `azure-network.json` (VPN) | [About Azure VPN Gateway](https://learn.microsoft.com/en-us/azure/vpn-gateway/vpn-gateway-about-vpngateways) | high |
| `azure-disks.json` | [Select a disk type for Azure IaaS VMs](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types) | high |
| `azure-vm.json` | one page per series: [Bv1](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/bv1-series), [Bsv2](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/bsv2-series), [Dsv5](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/general-purpose/dsv5-series), [Esv5](https://learn.microsoft.com/en-us/azure/virtual-machines/sizes/memory-optimized/esv5-series) | high |

Every number in every table was transcribed from the live page, not recalled. Nothing is marked `medium` or `low`, because anything that could not be read off a page was left out rather than estimated.

## What I could not verify, and what is therefore approximate

This is the part that matters most.

1. **The 32-port floor that makes AZ3 critical is borrowed from a different service.** Azure publishes no "minimum SNAT ports per instance" for NAT Gateway, because NAT Gateway allocates on demand and deliberately has no per-instance preallocation. The thresholds AZ3 uses — warning below 1,024 ports per endpoint, critical below 32 — are the maximum and minimum of Azure *Load Balancer's* default preallocation table. That is Microsoft's own arithmetic for how many ports one instance needs, and it is the closest published yardstick, but it is not a NAT Gateway number. It is stated in the finding text and in the catalog notes so the reader can disagree with the threshold without disbelieving the arithmetic.

2. **AZ3's endpoint count is an upper bound treated as the demand.** Every pod is counted as a source of outbound flows. In practice many pods never egress to the internet at all. The finding says "at full scale", which is honest, but a cluster where only a handful of workloads talk outbound will read worse than it is.

3. **AZ1's topology signal is weaker than R1's.** On AWS the rule walks security groups, and an ingress rule naming another security group is a declared, trustworthy network edge. Azure puts a database behind a delegated subnet or a private endpoint, and neither states who may talk to it. AZ1 therefore uses only one signal: the application naming the server in its configuration. That misses an app that reads its connection string from Key Vault at runtime — an increasingly common and entirely correct pattern — so **AZ1 under-reports rather than over-reports**. Traversal through `azurerm_private_endpoint` was considered and rejected: a private endpoint says a virtual network can reach the server, not that any particular workload does, and inferring "same VNet, therefore connects" would manufacture consumers.

4. **PostgreSQL `max_connections` is fixed at provisioning time.** Microsoft is explicit that the value is computed from the product name when the instance is created and is *not* recomputed on a later resize. The catalog therefore describes what a freshly created server gets, which is exactly what a plan creates — but running `headroom` against a plan that resizes an existing server will quote the new SKU's ceiling when the real one is still the old SKU's. There is nothing in the plan JSON that would reveal this.

5. **MySQL's ceiling is soft.** Unlike PostgreSQL, MySQL flexible server allows `max_connections` to be raised to roughly twice the default. The rule quotes the default and says so, but it is a weaker claim than the PostgreSQL one.

6. **Microsoft's own MySQL table is internally inconsistent.** `Standard_B2ms` is listed with 4 GB of physical memory on the server-parameters page and 8 GiB on the compute page, while every other row's connection count tracks memory. The connection figure (683) is recorded as printed. If the memory figure is the typo, the connection count is right; if the connection count is the typo, this entry is wrong. It is noted in the catalog file.

7. **VPN gateway generation is not in the Terraform.** `VpnGw2AZ` and `VpnGw3AZ` exist as both Generation1 and Generation2 SKUs with different throughput (1 Gbps vs 1.25 Gbps, 1.25 vs 2.5). `azurerm_virtual_network_gateway` states only the SKU name. The catalog carries the Generation2 (faster) figure, which makes AZ4 understate the problem rather than overstate it, but on a Generation1 gateway the quoted ceiling is up to 2x too generous.

8. **`azure-vm.json` covers four series out of well over a hundred.** Bv1, Bsv2, Dsv5 and Esv5. Anything else has no encoded ceiling and AZ5 and AZ6 stay silent about it rather than interpolate. This is the single largest gap in Azure coverage and it is pure transcription work: one Microsoft page per series.

9. **Cached disks are not evaluated by AZ5.** Host caching moves a disk onto a separate, larger VM limit that the catalog does not carry. Rather than compare a cached disk against the uncached cap (which would be wrong in the customer's favour, and therefore still wrong), AZ5 skips it. A VM whose disks are all cached produces no finding even if it is badly oversized.

10. **AZ2 understates demand.** Microsoft's documented formula is `(nodes + max surge) + ((nodes + max surge) * max pods per node)`. AZ2 leaves the surge nodes out, because `upgrade_settings.max_surge` can be a percentage and resolving it correctly is more precision than the finding needs. The real demand during a rolling upgrade is higher than the number quoted, which the finding says.

11. **A node pool that sets `pod_subnet_id` is skipped, not guessed at.** The reference graph is keyed per block rather than per attribute, so when a pool names both `vnet_subnet_id` and `pod_subnet_id` the two references cannot be told apart, and attributing node demand to the pod subnet would be badly wrong. Such a pool produces an explicit info finding naming what was skipped. Fixing it properly means keying edges per attribute in `internal/graph`.

## Offline planning for azurerm

The AWS provider has `skip_credentials_validation`. **azurerm has no equivalent**, and offline planning is genuinely harder. It does work, and both fixtures are real Terraform producing a real plan, but it takes three things together:

1. **A token that parses.** The provider acquires an access token at configure time and reads the tenant out of its claims before it will plan anything, so fake ids alone fail with `AADSTS90002: Tenant not found`. `fixtures/azure-01-aks-postgres/fake-imds.py` is a ~60-line stdlib Python stub that serves an unsigned JWT with the right claims; the provider is pointed at it with `use_msi = true` and `msi_endpoint`. Nothing in the fixtures calls ARM afterwards, so the token only has to parse, not authenticate.
2. **`resource_provider_registrations = "none"`** in the provider block, to stop the registration calls.
3. **`ARM_PROVIDER_ENHANCED_VALIDATION=false`** in the environment. This is the non-obvious one: even with registrations disabled, the provider populates a resource-provider cache from ARM for location validation, which fails with `SubscriptionNotFound`. Only this environment variable suppresses it, and it has no provider-block equivalent.

Regenerating either fixture:

```powershell
cd fixtures/azure-01-aks-postgres
python fake-imds.py            # in another shell; listens on 127.0.0.1:47712
terraform init
$env:ARM_PROVIDER_ENHANCED_VALIDATION = 'false'
terraform plan -out=tfplan
terraform show -json tfplan > plan.json
```

`fixtures/azure-02-vm-disk-vpn` uses the same stub and the same environment variable.

Two further constraints the fixtures work around:

- **No data sources.** Every `azurerm` data source is a live API call and would defeat all of the above.
- **One unknown value poisons a whole map.** `app_settings` is a `map(string)`; referencing `azurerm_postgresql_flexible_server.main.fqdn` (unknown until apply) makes Terraform mark the entire map unknown, which would hide `DB_POOL_SIZE` from `planned_values` too. The fixture builds the host from `.name`, which is known at plan time, so both the reference edge and the pool size survive. This is worth knowing for real customer plans as well: **AZ1 loses the declared pool size whenever an app setting anywhere in the same map depends on an unknown value**, and drops to `medium` confidence with the `--pool-size` assumption. It cannot tell the difference between "not declared" and "declared but unknowable at plan time".

## Privacy

`internal/extract/azure_allowlist.go` adds the Azure types to the same allowlist, and the omissions are the interesting part. Absent on purpose: `custom_data` and `user_data` (arbitrary boot scripts, routinely secrets), `admin_password` and `admin_ssh_key`, `shared_key` (VPN pre-shared keys), `tags` (free text, and where organisations put owner names and ticket numbers), and identifiers like `name`, `fqdn` and `dns_prefix`.

`app_settings` and `connection_string` are also absent, and they are the exact Azure analogue of `container_definitions` on the AWS side: azurerm's usual home for connection strings and API keys. AZ1 reads `app_settings` **locally** to derive the pool size, and only the resulting integer travels. Verified by running `analyze --dry-run --salt local-dev` over both fixtures and grepping the payload for every secret and identifier in them — nothing leaks.

## Requests for files I do not own

Two, both small, neither blocking.

1. **`README.md` rules table.** The Azure rules are not listed there. Suggested rows:

   | AZ1 | Flexible server connections: `max_workers x pool_size` vs `max_connections(sku_name)` | ✅ |
   | AZ2 | AKS node subnet exhaustion: Azure CNI takes one address per pod, not per node | ✅ |
   | AZ3 | NAT gateway SNAT ports: 64,512 per public IP divided across every endpoint behind it | ✅ |
   | AZ4 | VPN connections on one gateway share the SKU throughput, they do not add to it | ✅ |
   | AZ5 | Managed disk tier provisions performance the VM size cannot drive | ✅ |
   | AZ6 | B-series baseline, and the fact that Azure has no unlimited mode | ✅ |

   The "AWS only" line under *Known limits in v0* also needs updating.

2. **`internal/graph/graph.go`: per-attribute edge keying.** `ReferencesIn` resolves references per *block*, so two attributes inside one block (`vnet_subnet_id` and `pod_subnet_id` on an AKS node pool) are indistinguishable. AZ2 currently skips such pools and says so. This is the same class of limitation already recorded in the README as "instance values collapse onto the block", and a fix would let AZ2 handle pod subnet mode, which is the *recommended* Azure CNI configuration and so will only get more common.

## Verification

```powershell
$env:Path += ';C:\Users\Marcus\sdk\go\bin'
$env:GOOS='linux'; $env:GOARCH='amd64'
go test -c -o bin/az.test ./internal/rules
go build -o bin/headroom-az ./cmd/headroom
$env:GOOS=''; $env:GOARCH=''

wsl -e bash -c "cd /mnt/d/Projetos/headroom/internal/rules && /mnt/d/Projetos/headroom/bin/az.test -test.v -test.run Azure"
wsl -e bash -c "cd /mnt/d/Projetos/headroom && ./bin/headroom-az analyze fixtures/azure-01-aks-postgres/plan.json"
```

`bin/az.test` is a distinct binary name so it does not race the AWS or GCP test binaries.
