# Changelog

Notable changes per release. Dates are the tag date.

## v0.2.0, 2026-08-16

Carries three pull requests: `--upload`, colour in the report, and the work
below.

The release that came out of running the tool against seven real production
plans from a live Azure estate. It reported nothing on all seven and exited 0 on
all seven, including under `--fail-on warning`, and the infrastructure was not
idle: one of those plans puts 51,480 provisioned IOPS and 6,000 MB/s in front of
a VM whose sustained ceiling is 33,333 and 992.

Seven for seven was not a tuning problem. Almost everything below is a reason
the tool had for saying nothing.

### Breaking changes

**A plan that was silent may now produce findings, and `--fail-on` acts on
them.** This is the point of the release and it is also the thing most likely to
turn a green pipeline red without any infrastructure having changed. If you gate
on `--fail-on`, expect to look at new findings before this upgrade rolls out
everywhere. Run once without the gate first.

**`--json` emits `[]` instead of `null` when there are no findings.** Anything
that special-cased the literal `null` needs to stop. Anything doing
`jq 'length'` starts working, which was the point: the shape of the document
used to change with the result.

**The uploaded payload carries a new field, `node.instance_count`.** A server
built against the previous ingest schema enforces `additionalProperties: false`
and rejects the whole report with a 400. Upgrade the receiving end before
upgrading the CLI, if you run your own.

**AZ5 finding text changed**, both title and summary, because it was claiming
more than the data supports for two of the three disk kinds. Anything matching on
the text of the report rather than on `--json` will not match.

**`xid` now covers Azure and GCP.** AWS identifiers hash exactly as before, on
purpose, so stored joins survive the upgrade. Azure and GCP resources that never
produced an `xid` now produce one.

### Fixed

- **The reference graph stops at a module boundary.** A reference into a module
  resolved to the module call, which has no type, so every rule that reasons
  about a relationship between two resources went quiet whenever the two lived in
  different modules. That is the normal arrangement in any repository of any
  size, and it affected fifteen rule files. The graph now follows a module
  output through to the resource behind it, including through nested modules and
  through a module indexed dynamically, where terraform records no output name at
  all.
- **A block whose value is `each.value` states its reference in `for_each`.**
  Reading only the block left the edge unresolved on entirely idiomatic
  terraform.
- **AZ2 read only the azurerm 4.x spelling of the autoscaling flag.** Against a
  3.x plan it fell back to `node_count` and then stated "no autoscaling" about a
  pool that autoscales, and a separate node pool that declares no `node_count`
  disappeared from the analysis. Measured on the same cluster planned both ways:
  333 addresses of demand against 2,220.
- **Azure VM size and VPN gateway lookups were case sensitive** while the disk
  lookup in the same file was not. Azure is not, so a valid size written with a
  different capital was unknown to the catalog with the correct figure sitting in
  the table.
- **AZ5 called every excess "provisioned and billed".** Standard SSD figures are
  documented as "up to" and Premium SSD v2 does not price off the tier ladder,
  so the money claim now prints only when every disk in the sum is a provisioned
  tier. The ceiling claim is arithmetic and always prints.
- **A Premium SSD v2 disk that declares no IOPS counted as zero.** Every one of
  them gets 3,000 IOPS and 125 MB/s free of charge, and terraform makes both
  attributes optional, so declaring nothing is the common case.
- **`azurerm_virtual_machine` was invisible to AZ5 and AZ6.** The pre split
  resource declares its disks inside the VM block and azurerm 4.0 removed it,
  which does nothing about the estates that still plan it under 3.x.
- **`--json` on a clean run emitted `null`.**
- **The uploaded payload collapsed every `for_each` instance into one node**, so
  three disks arrived as one. The local report was never affected, which is why
  nothing caught it.
- **Cross repository stitching recognised AWS identifiers only.**

### Added

- **`--upload`**, which sends the redacted payload to the headroom API after
  printing the local report. It refuses to run alongside `--dry-run`, refuses to
  send anything without a salt and has no flag that permits it, and refuses
  without an API key. The bytes it sends are the bytes `--dry-run` prints, from
  one function and one call site, so a customer can audit an upload by
  rehearsing it. When the gate fires and the upload fails, exit 1 wins over exit
  2: a network failure must not be able to launder a finding into a shrug.
- **Colour in the terminal report.** Severity is coloured and nothing else is,
  in eight colour ANSI. Stripping the escapes returns the uncoloured report byte
  for byte. `NO_COLOR` is honoured, `--no-color` turns it off, and colour is off
  automatically when stdout is not a terminal.
- **`--explain`**, which writes to stderr what each rule did with this plan:
  what it reported, what it looked at and could not use, and why. The empty
  report has always told the reader to run it and the flag did not exist, so
  following the only instruction the tool gives printed a parse error and exited
  2.
- **Three fixtures**: a plan split across modules, the same AKS cluster planned by
  azurerm 3.x, and the pre split VM resource. Every fixture before these was flat
  and single provider, which is how the module boundary defect survived 199 green
  tests.
- **61 VM sizes in the Azure catalog**, in six series: Esv6, Esv4, Easv5,
  FXmdsv2, FXmsv2 and Basv2. Each carries `source`, `verified_at` and
  `confidence`, and the reasoning for every figure is in
  `docs/catalog-verification.md`, including the three traps: the FX local storage
  tab publishes 150,000 IOPS of ephemeral NVMe next to a 33,000 IOPS remote
  ceiling, the AMD burstable halves at 32 vCPUs where the Intel one does not, and
  Esv4 and Easv5 share the Esv5 IOPS with lower throughput.
- **A second uncached ceiling** for the series that publish one, which governs
  Ultra Disk and Premium SSD v2 and is about a third higher than the Premium SSD
  pair. Judging a v2 disk against the lower pair understates the ceiling, and
  understating a ceiling is how a rule invents a finding.

### Internal

- `internal/extract` had no tests. It has some now.
- `run.ps1 test` summarises and colours instead of printing every test, with
  `-Detailed` for the old behaviour.

## v0.1.0, 2026-08-14

First release. Rules R1 to R8 on AWS, AZ1 to AZ6 on Azure and GC1 to GC6 on
Google Cloud, a catalog where every figure carries the vendor page it came from,
`--json`, `--fail-on` and `--dry-run`, signed archives with an SBOM per file.
