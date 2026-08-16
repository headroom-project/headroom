# headroom CLI: what a real corpus found, and how it gets fixed

> Written 2026-08-15, after running the CLI against seven real production plans from a
> live Azure estate. No customer identifier appears in this document, by contract. The
> evidence below is reproducible from any Terraform that uses modules.

## What this is

The CLI was run against seven distinct production plans, six generated live against an
empty state and one decoded from a saved plan file. It found nothing in any of them, and
exited 0 in all seven, including under `--fail-on warning`.

The infrastructure was not idle. One plan provisions five uncached Premium SSD v2 disks
totalling 51480 IOPS and 6000 MB/s behind a VM whose sustained remote storage ceiling is
33333 IOPS and 992 MB/s. Six times the throughput it can drive, above even the burst
figure. The tool walked past it without a word.

Seven for seven is not a tuning problem. This document is the fix.

**Scope: the CLI, and nothing else.** No rule is added here, no customer configuration is
corrected here, no cloud is adopted here. Those are tracked elsewhere and deliberately
kept out.

## The one thing that shapes all of it

**The reference graph stops at a module boundary.**

`internal/graph/graph.go:230-234` normalizes any reference beginning with `module.` to
`"module." + parts[1]`, which is the address of the module *call*. That address has no
entry in `g.types`, so `ReferencesIn` drops it at `graph.go:124`. In a real plan the
attribute arrives as `{"references": ["module.database", ...]}` and dies there.

Every rule that reasons about a relationship between two resources is therefore blind
whenever the two live in different modules, which is the normal arrangement in any
corporate repository. That is fifteen rule files: R001, R002, R004, R006, R008, AZ1, AZ3,
AZ4, AZ5 and five GCP rules.

It survived 199 green tests because **none of the eight fixtures uses a module**. Every
`plan.json` under `fixtures/` contains zero addresses beginning with `module.`. The suite
proves the code does what the fixtures say, and the fixtures are flat.

This is the same lesson the project has now learned four times, in a fourth costume. It is
why milestone 1 below is a test and not a fix.

## Invariants

The properties the tests exist to prove. Everything else is behaviour.

1. **A verdict does not depend on module layout.** The same resources, the same
   attributes, the same numbers, produce the same finding whether they are declared in one
   root module or split across three. This is the invariant milestone 4 buys, and the one
   most likely to regress.
2. **Silence is attributable.** A rule that stays quiet because it could not anchor its
   data is distinguishable, by the user and by a test, from a rule that stayed quiet
   because the resource has headroom. Every skip carries a named reason.
3. **The escape hatch printed in the output exists.** No message tells the user to run a
   flag that the flag set does not define.
4. **Machine readable output is valid JSON of the documented shape, empty included.** An
   empty result is `[]`. Never `null`.
5. **A lookup is case insensitive wherever the vendor is case insensitive.** Azure does not
   distinguish case in a VM size, so neither does the catalog.
6. **A provider attribute rename never changes a verdict silently.** When the tool cannot
   tell whether autoscaling is on, it says so. It never asserts the opposite of what the
   plan declares.
7. **A missing ceiling beats an uncertain ceiling.** Unchanged, and it constrains
   milestone 7: a documented free baseline is a known figure, an undeclared one is not.
8. **One payload node per resource instance.** Three disks are three nodes. This is what
   milestone 9 restores.
9. **`--dry-run` bytes and uploaded bytes are the same bytes, from the same function.**
   Unchanged, and milestone 9 must not weaken it.

## Milestones

| # | Deliverable | Done when |
|---|---|---|
| 1 | Modular fixture | A fixture whose VM and disk attachment live in different modules is committed, and the missing finding is a **failing test, red before any fix** |
| 2 | `--explain` | The flag exists, prints every skip with its reason, and the empty report points at something real |
| 3 | Three small ones | `[]` not `null`, case insensitive size lookup, honest finding wording |
| 4 | The graph crosses module outputs | Milestone 1 goes green, and nothing else changes verdict without a reason |
| 5 | Regression sweep | The private corpus of real plans is re-run and every new finding is confirmed by hand against vendor documentation |
| 6 | Provider version tolerance | An azurerm 3.x plan and a 4.x plan of the same topology produce the same verdict |
| 7 | Premium SSD v2 baseline | An undeclared Premium v2 disk contributes its documented free baseline, and says that is what it did |
| 8 | Legacy `azurerm_virtual_machine` | The pre split resource type is analysed by AZ5 and AZ6, including its own os disk shape |
| 9 | Payload fidelity | Instances stop collapsing, stitching stops being AWS only, and the API conformance corpus is regenerated |
| 10 | Catalog series | The sizes the field actually writes are in the catalog, each with `source`, `verified_at` and `confidence` |

Milestone 1 comes first on purpose. The defect is already there. The useful artifact is a
test that shows it before any code exists to hide it.

Milestones 4 and 9 are the two that carry real risk, for opposite reasons: 4 can invent
false positives across fifteen rules at once, and 9 changes the contract with the API.

---

### M1. The modular fixture

A fixture in the existing style: real Terraform producing a real plan, committed, never
hand edited. It declares a VM in one module and its data disk attachments in another, with
the disk ids crossing the boundary through module outputs, because that crossing is the
thing under test.

**The fixture is ours, authored from scratch.** No customer plan enters this repository, in
any form, redacted or not. The real corpus is a private regression surface for M5 and never
a committed artifact.

Numbers are chosen so the answer is arithmetic, not judgement: a VM with a documented
ceiling, uncached disks that clearly exceed it, and a second VM in the same plan that
clearly does not, so the fixture proves both the finding and the silence.

**Done when:** the assertion for the expected finding fails against `main`, and the failure
message names the module boundary rather than the number.

### M2. `--explain`

`internal/report/report.go:31` tells the user to run `--explain`, and `cmd/headroom/main.go`
never defines it. Following the instruction exits 2.

The fix is to build it, not to delete the sentence. In seven of seven real plans the
silence was a false negative, and `--explain` is the only thing the user reads when the
tool has nothing to say. It stops being cosmetic and becomes the instrument that makes M4
diagnosable in the field.

It prints, per rule, what the rule looked for, what it found, and where it stopped: the
resource it anchored on, the neighbour it could not resolve, and the reason. It is a report
about the analysis, not about the infrastructure, so it goes to stderr and leaves stdout
holding the report and only the report.

**Done when:** on the fixture from M1, before M4 lands, `--explain` says the attachment was
not reachable from the VM. That sentence is the bug report the user could have filed.

### M3. The three small ones

**`[]` not `null`.** `cmd/headroom/main.go:177` encodes a nil slice, so a clean run emits
`6e75 6c6c 0a`. It breaks `jq 'length'` in the most common case there is, the green
pipeline. `internal/extract/extract.go:117-119` already initializes its slices for exactly
this reason; the `--json` path never got the same care.

**Case insensitive size lookup.** `internal/catalog/azure.go:465` does a bare map lookup
while `AzureDiskTierFor` at line 430 lowercases first. Two lookups in one file with two
rules. Azure treats VM sizes case insensitively, so a size written with different case is
valid input that the tool silently fails to recognise, with the correct figure already sat
in the catalog. `AzureVPNGateway` has the same shape, and a lowercase gateway sku is common
in the field.

**Honest wording.** For Standard SSD the vendor says "up to", so summing tier figures and
calling the total provisioned and billed claims more than the data supports. For Premium v2
the baseline is free, so exceeding a VM ceiling there is a performance ceiling and not
wasted spend. Two different claims, currently one sentence.

**Done when:** each has a test that fails without it, including one that feeds the three
real world spellings of a size that is already in the catalog.

### M4. The graph crosses module outputs

Resolve a reference to a module output through to the resource that produces it, using
`configuration.root_module.module_calls[].outputs` together with `planned_values`, and keep
resolving while the output is itself another module output. A reference that cannot be
resolved to a typed resource stays unresolved and is reported by `--explain`, never guessed.

Two things this must not do. It must not merge instances: `module.x["a"]` and
`module.x["b"]` are different resources and an edge into one is not an edge into the other.
And it must not invent an edge where the plan only shows a value passing through a
variable, because a rule that fires on a coincidence is the failure mode that costs the
customer.

**Done when:** M1 is green, the full suite is green, and every fixture that changed verdict
is explained.

### M5. The regression sweep

Re-run every real plan in the private corpus and diff against the pre M4 output. Each new
finding is confirmed by hand against the vendor page that states the ceiling before it is
accepted. A new finding that cannot be confirmed is a false positive and blocks M4 from
being called done.

This step is not optional and not delegable to a green suite. M4 changes the behaviour of
fifteen rule files at once, and the entire premise of this document is that a green suite
was not evidence.

**Done when:** every delta is either confirmed against documentation or fixed.

### M6. Provider version tolerance

`internal/rules/azure.go:329` reads `auto_scaling_enabled`, which is the azurerm 4.x name.
In 3.x the attribute is `enable_auto_scaling`. Against a 3.x plan the rule reads false,
falls back to `node_count`, and prints `node_count, no autoscaling` about a pool that is
autoscaling. A separate node pool that declares only `min_count` and `max_count` ends up
with `maxNodes = 0` and disappears from the analysis entirely.

Measured on a controlled pair with identical topology: 4.x reports CRITICAL at a demand of
1776, 3.x reports nothing at a demand of 222. Eight times out, on the wrong side of the
threshold.

This is the only defect in the set where the tool states the opposite of what the plan says
rather than staying quiet, which makes it the worst one on the list regardless of its size.

Read both names, newer first. Then audit every attribute the Azure rules read against the
4.0 rename, because one instance of a class is rarely alone.

**Done when:** the same topology planned under 3.x and under 4.x produces the same verdict,
as a test with both plans committed.

### M7. Premium SSD v2 baseline

`AzureDiskTierFor` excludes Premium v2 and Ultra on purpose, because they carry their own
IOPS on the resource, and `internal/rules/az5_disk_vm.go:155-156` reads
`disk_iops_read_write` and `disk_mbps_read_write`. When the customer does not declare them,
both read zero and the disk enters the sum as if it consumed nothing.

Not declaring them is the common case, not the exception: the disk still gets a documented
free baseline. Encode that baseline as a catalog entry with its own `source` and
`verified_at`, use it when the attributes are absent, and have the finding say the figure
came from the baseline rather than from the plan. Invariant 7 holds: this is a documented
figure, not a guess.

**Done when:** a plan with an undeclared Premium v2 disk produces the same finding as the
same plan with the baseline written out, and the text distinguishes the two.

### M8. Legacy `azurerm_virtual_machine`

AZ5 and AZ6 iterate only `linux_virtual_machine` and `windows_virtual_machine`. The pre
split type is still widely deployed, and its os disk uses a different shape,
`storage_os_disk` with `managed_disk_type`, which the current os disk loop does not read.

Three independent changes: the type joins both loops, the os disk shape is handled, and any
size the legacy resources use joins M10.

**Done when:** a fixture using the legacy type produces the same verdict as the equivalent
fixture using the split types.

### M9. Payload fidelity, and the contract with the API

`internal/extract/extract.go:130-134` reduces an address with `plan.Base` and then dedupes
on it, so every instance of a `count` or `for_each` resource collapses into one node. Three
disks of 3000 IOPS upload as one. The local rules escape this because they use
`graph.Instances`; what reaches the API does not. Same class as the incident where eight of
eight real payloads were refused.

`internal/extract/stitch.go:83` prefixes a literal `"aws:"` and lines 102-104 recognise only
AWS id patterns, so on Azure and GCP `xid` is never emitted and `external` and
`remote_states` stay empty even when the prior state carries everything needed. The cross
repository view, which is the paid premise, does not exist outside one cloud.

This milestone changes the payload, so by the rule that binds the two repositories the
conformance corpus is regenerated and the API suite is run. Invariant 9 is retested
explicitly: one function, one call site, identical bytes.

**Done when:** the corpus is regenerated, the API suite is green, and a plan with three
instances uploads three nodes.

### M10. Catalog series

The catalog covers Bv1, Bsv2, Dsv5 and Esv5. The field writes v4, v6, the AMD `as_v5` and
`as_v6` lines, FX, and Basv2. In a sample of eleven real stacks with material, eight were
false negatives purely because the size was absent.

One series at a time, each entry carrying `source`, `verified_at` and `confidence`, with
`source` a deep link to the vendor page that states the number. A figure that cannot be
found in official documentation stays out and the rule stays quiet, unchanged.

Order by observed frequency: Esv6, Esv4, Easv5, FX, Basv2.

**Done when:** `docs/catalog-verification.md` covers every added entry, and a regression
test asserts the count of encoded sizes so a silent deletion is red.

## Ordering

M1 before M2 before M4, because the fixture defines the failure, `--explain` describes it in
the tool's own words, and only then is there something to fix. M5 is welded to M4 and they
land together or not at all. M3 is independent and can go first if a small green commit is
useful. M6, M7 and M8 depend on neither the graph nor each other, so they parallelize. M9 is
last of the code changes because it reaches into the other repository. M10 is continuous.

## Out of scope, deliberately

- **New rules.** Subnet exhaustion outside AKS, load balancer SNAT, hub and spoke peering
  limits, and a generic connection limit rule for L4 proxies are all justified by the same
  corpus, and all of them are product backlog rather than defect repair. They do not belong
  in a document about fixing what is broken.
- **A fourth cloud.** The corpus that motivated this work is mostly a cloud the tool does not
  support. It stays unsupported: the quota figures are not declared in configuration, and the
  instance types are private names with no public documentation, so they cannot satisfy the
  catalog rule. Nothing about that changes here.
- **Anyone's Terraform.** The corpus surfaced real defects in the configuration it was run
  against. They were reported to its owner and none of them are this project's to fix.

## How this gets verified on the build machine

Application Control blocks unsigned binaries, so Go compiles and does not execute.

```powershell
.\run.ps1 test      # every package with tests, cross compiled and run through WSL
.\run.ps1 analyze   # one fixture
```

A single green run is not evidence, and this document exists because of that.

## State, 2026-08-16

Every milestone in the table above landed on `fix/m3-small-corrections`, in the
order M3, M1, M4 with M5, M6, M8, M7 with M10, M9, M2. The suite went from 143
tests to 156 and was run three times at the end rather than once, because one
green run has never been evidence in this project.

Four things were found while doing it that this document did not predict, and
all four are in the commits:

**A module indexed dynamically carries no output name at all.** Terraform
records `module.vms[each.value.name].id` as a reference to the bare call, so the
output name is nowhere in the plan. M4 as specified would still have been quiet
on the estate that motivated it. A bare call is now expanded through every
output it declares, and the expansion refuses to choose when more than one
result matches the type asked for.

**A block that says `each.value` states its reference in `for_each`.** Reading
only the block leaves the edge unresolved on terraform that is entirely
idiomatic. It is the same defect as the module boundary wearing different
clothes, and the same fixture covers both.

**The payload allowlist had never heard of `azurerm_virtual_machine`.** M8 taught
two rules about the pre split resource and the finding they produced then
referenced nothing, so the API rejected the whole report with a 400. The
conformance corpus caught it on the first run.

**An Azure `kind` carries a dot and a slash**, into a field the ingest contract
bounds to `[a-z][a-z0-9-]*` precisely so that a terraform address or an ARN
cannot travel in it. Fixed on the CLI side rather than by loosening the
contract, and the CLI now carries the contract's own pattern so it cannot emit a
value the API would reject.

The regression sweep of M5 is what turned this from plausible to true. The
private corpus went from 0 findings across 7 real plans to 5, every one of them
checked by hand against the vendor page that states the ceiling, and no false
positive. The largest is 51,980 provisioned IOPS and 6,100 MB/s behind a size
that drives 33,333 and 992.
