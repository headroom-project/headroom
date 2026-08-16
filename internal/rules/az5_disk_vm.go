package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// azDiskVMAsymmetry compares what the disks attached to a virtual machine
// provision against what the virtual machine is allowed to drive.
//
// Azure sells disk performance in fixed size tiers and virtual machine
// performance in its own separate ladder, and the two are never mentioned in
// the same place. A 1 TiB Premium SSD is a P30: 5,000 IOPS, provisioned,
// billed, and completely unreachable behind a Standard_B2s, which caps uncached
// traffic at 1,280. Nothing fails, nothing warns, the money is spent and the
// application is slow for a reason that is in neither resource.
//
// The rule reports uncached disks only. Host caching moves a disk onto a
// different, larger VM limit that this catalog does not carry, and comparing it
// against the uncached number would be wrong in the customer's favour, which is
// still wrong.
func azDiskVMAsymmetry(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, vmType := range []string{
		"azurerm_linux_virtual_machine",
		"azurerm_windows_virtual_machine",
		// The pre split resource, removed in azurerm 4.0 and still planned by
		// every estate pinned to 3.x. Same question, different spelling.
		"azurerm_virtual_machine",
	} {
		for _, vm := range f.ByType(vmType) {
			addr := plan.Base(vm.Address)
			sizeName := azVMSizeOf(vm.Values)

			size, ok := c.AzureVMSizeOf(sizeName)
			if !ok {
				opt.Trace.Skip("AZ5", addr, "the size "+quoteOrNone(sizeName)+
					" has no encoded ceiling, so there is nothing to compare the disks against")
				continue
			}

			iops, mbps := 0, 0.0
			var detail []string
			resources := []string{addr}
			counted := 0
			tierSource := ""

			// What kind of number went into the sum, because the sentence at the
			// end depends on it. A Premium SSD tier is provisioned and billed. A
			// Standard SSD tier is documented as "up to", which the catalog note
			// says in as many words. Premium v2 and Ultra state their own figures
			// and do not price off the tier ladder. Only the first of the three
			// supports a claim about money.
			billedTier, upToTier, declared := false, false, false
			upToNote, baselineNote := "", ""

			// Whether any Premium SSD v2 or Ultra disk is in the sum, because
			// newer sizes publish a separate and higher ceiling for those.
			v2Present := false

			for _, disk := range azUncachedDisks(g, addr, vmType) {
				tier, known := c.AzureDiskTierFor(disk.accountType, disk.sizeGiB)
				if !known {
					// Premium SSD v2 and Ultra carry their own numbers on the
					// resource, so there is nothing to look up. What is left
					// unstated is not nothing: every Premium SSD v2 disk gets a
					// free baseline whether or not the terraform mentions it,
					// and reading the silence as zero is what made this rule walk
					// past terabytes of storage.
					diskIOPS, diskMBps, fromBaseline := disk.iops, disk.mbps, false
					if base, ok := c.AzurePremiumV2Baseline(); ok && catalog.AzureIsPremiumV2(disk.accountType) {
						if diskIOPS == 0 {
							diskIOPS, fromBaseline = base.IOPS, true
						}
						if diskMBps == 0 {
							diskMBps, fromBaseline = base.MBps, true
						}
						baselineNote = base.Notes
					}
					if diskIOPS > 0 || diskMBps > 0 {
						iops += diskIOPS
						mbps += float64(diskMBps)
						counted++
						declared = true
						if catalog.AzureIsPremiumV2(disk.accountType) {
							v2Present = true
						}
						if !disk.inline {
							resources = append(resources, disk.addr)
						}
						how := "states %d IOPS and %d MB/s on the resource"
						if fromBaseline {
							how = "runs at the free baseline of %d IOPS and %d MB/s, which the plan never states because it never has to"
						}
						detail = append(detail, fmt.Sprintf(
							"%s (%s, %d GiB) "+how+".",
							disk.addr, disk.accountType, disk.sizeGiB, diskIOPS, diskMBps))
					}
					continue
				}
				iops += tier.IOPS
				mbps += float64(tier.MBps)
				counted++
				tierSource = tier.Source
				if !disk.inline {
					resources = append(resources, disk.addr)
				}
				if tier.Kind == "Standard SSD" {
					upToTier, upToNote = true, tier.Notes
					detail = append(detail, fmt.Sprintf(
						"%s is %d GiB of %s, which Azure bills as a %s and serves at up to %d IOPS and %d MB/s.",
						disk.addr, disk.sizeGiB, tier.Kind, tier.Tier, tier.IOPS, tier.MBps))
					continue
				}
				billedTier = true
				detail = append(detail, fmt.Sprintf(
					"%s is %d GiB of %s, which Azure bills and serves as a %s: %d IOPS and %d MB/s.",
					disk.addr, disk.sizeGiB, tier.Kind, tier.Tier, tier.IOPS, tier.MBps))
			}

			if counted == 0 {
				opt.Trace.Skip("AZ5", addr, "no uncached disk could be resolved from this plan: "+
					"an attachment that could not be followed, a disk declared elsewhere, or every disk cached")
				continue
			}

			// Which ceiling the size imposes depends on what is attached to it.
			// A series that publishes a second uncached pair for Ultra Disk and
			// Premium SSD v2 drives those disks harder, by around a third, and
			// measuring them against the Premium SSD pair would manufacture a
			// finding out of a column heading. A series that publishes one pair
			// governs every disk type with it, so nothing changes there.
			capIOPS, capMBps := size.UncachedIOPS, size.UncachedMBps
			burstIOPS, burstMBps := size.BurstIOPS, size.BurstMBps
			capKind := ""
			if v2Present && size.UncachedV2IOPS > 0 {
				capIOPS, capMBps = size.UncachedV2IOPS, size.UncachedV2MBps
				burstIOPS, burstMBps = size.BurstV2IOPS, size.BurstV2MBps
				capKind = " for Ultra Disk and Premium SSD v2, which this size drives harder than Premium SSD"
			}

			overIOPS := capIOPS > 0 && iops > capIOPS
			overMBps := capMBps > 0 && mbps > capMBps
			if !overIOPS && !overMBps {
				opt.Trace.Skip("AZ5", addr, fmt.Sprintf(
					"%d uncached disk IOPS and %.0f MB/s against a ceiling of %d and %.0f: within its limits",
					iops, mbps, capIOPS, capMBps))
				continue
			}

			detail = append(detail, fmt.Sprintf(
				"%s is a %s, which drives at most %d uncached IOPS and %.0f MB/s%s across every uncached disk attached to it.",
				addr, size.Name, capIOPS, capMBps, capKind))
			if burstIOPS > capIOPS {
				detail = append(detail, fmt.Sprintf(
					"The size can burst to %d IOPS and %d MB/s for up to 30 minutes at a time, so a benchmark will not show this and a sustained workload will.",
					burstIOPS, burstMBps))
			}
			// Only a Premium SSD tier is provisioned and billed. Mixing any other
			// kind into the sum makes a claim about money unsupportable, so the
			// finding drops the claim rather than qualifying it into mush.
			paidFor := billedTier && !upToTier && !declared

			detail = append(detail, "Catalog note: "+c.AzureStorageNotes())
			if upToTier && upToNote != "" {
				detail = append(detail, "Standard SSD note: "+upToNote)
			}
			if declared {
				detail = append(detail,
					"Premium SSD v2 and Ultra state their own IOPS and throughput on the resource and do not price off the tier ladder, so what follows is a ceiling this VM cannot reach, not a bill for performance that never arrives.")
			}
			if baselineNote != "" {
				detail = append(detail, "Premium SSD v2 baseline: "+baselineNote)
			}
			if paidFor {
				detail = append(detail,
					"Two ways out, and they cost differently: a larger VM size raises the cap, or a smaller disk tier stops paying for performance that cannot arrive. Which one is right depends on whether the capacity or the throughput was the point.")
			} else {
				detail = append(detail,
					"Two ways out: a larger VM size raises the cap, or a smaller disk stops offering performance that cannot arrive. Which one is right depends on whether the capacity or the throughput was the point.")
			}
			if tierSource != "" {
				// The finding quotes two documents. Source carries the VM one,
				// so the disk one has to be printed or the reader can only
				// check half the arithmetic.
				detail = append(detail, "Disk tiers: "+tierSource)
			}

			var problems []string
			if overIOPS {
				if paidFor {
					problems = append(problems, fmt.Sprintf("%d provisioned IOPS against a %d IOPS cap", iops, capIOPS))
				} else {
					problems = append(problems, fmt.Sprintf("%d disk IOPS against a %d IOPS cap", iops, capIOPS))
				}
			}
			if overMBps {
				if paidFor {
					problems = append(problems, fmt.Sprintf("%.0f MB/s provisioned against a %.0f MB/s cap", mbps, capMBps))
				} else {
					problems = append(problems, fmt.Sprintf("%.0f MB/s of disk against a %.0f MB/s cap", mbps, capMBps))
				}
			}

			// One claim always holds: the VM cannot drive it. The second claim,
			// that it is being paid for, only holds when every disk in the sum
			// is a provisioned tier.
			closing := "The disk performance above the VM cap can never be used."
			if paidFor {
				closing = "The disk performance above the VM cap is provisioned and billed and can never be used."
			}

			out = append(out, Finding{
				Rule:     "AZ5",
				Severity: SeverityWarning,
				Title:    "Disk offers performance the VM is not allowed to drive",
				Summary: fmt.Sprintf(
					"%s attaches %s to a %s: %s. %s",
					addr, azPlural(counted, "uncached disk", "uncached disks"), size.Name, join(problems), closing),
				Detail:     detail,
				Confidence: size.Confidence,
				Resources:  resources,
				Source:     size.Source,
				Metrics: map[string]int{
					"disk_iops":     iops,
					"vm_iops":       capIOPS,
					"disk_mbps":     azRound(mbps),
					"vm_mbps":       azRound(capMBps),
					"disks_counted": counted,
				},
			})
		}
	}
	return out
}

type azDisk struct {
	addr string
	// inline disks are declared inside the VM block and have no address of
	// their own, so the label above is for a human reading the detail and must
	// never be handed to anything that expects a terraform address.
	inline      bool
	accountType string
	sizeGiB     int
	iops        int
	mbps        int
}

// azUncachedDisks collects the data disks attached to a VM with host caching
// set to None, plus the OS disk when it is uncached. An attachment that asks
// for ReadOnly or ReadWrite caching is deliberately skipped: it counts against
// a different VM limit.
func azUncachedDisks(g *graph.Graph, vm, vmType string) []azDisk {
	var out []azDisk

	for _, attachment := range g.ReferrersOfType(vm, "azurerm_virtual_machine_data_disk_attachment") {
		if !contains(g.ReferencesIn(attachment, vmType, "virtual_machine_id"), vm) {
			continue
		}
		if caching := plan.Str(g.Values(attachment), "caching"); caching != "" && caching != "None" {
			continue
		}
		for _, disk := range g.ReferencesIn(attachment, "azurerm_managed_disk", "managed_disk_id") {
			for _, values := range g.Instances(disk) {
				size, _ := plan.Num(values, "disk_size_gb")
				iops, _ := plan.Num(values, "disk_iops_read_write")
				mbps, _ := plan.Num(values, "disk_mbps_read_write")
				out = append(out, azDisk{
					addr:        disk,
					accountType: plan.Str(values, "storage_account_type"),
					sizeGiB:     size,
					iops:        iops,
					mbps:        mbps,
				})
			}
		}
	}

	// The pre split azurerm_virtual_machine declares its disks inside the VM
	// block, under different names: storage_os_disk and any number of
	// storage_data_disk, both carrying managed_disk_type where the newer
	// resources say storage_account_type. None of it is reachable through an
	// attachment, so a rule that only walks attachments sees a VM with no disks.
	out = append(out, azInlineDisks(g, vm, "storage_os_disk", "managed_disk_type")...)
	out = append(out, azInlineDisks(g, vm, "storage_data_disk", "managed_disk_type")...)

	for _, values := range g.Instances(vm) {
		disks, ok := values["os_disk"].([]any)
		if !ok {
			continue
		}
		for _, item := range disks {
			osDisk, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if caching := plan.Str(osDisk, "caching"); caching != "None" {
				continue
			}
			size, ok := plan.Num(osDisk, "disk_size_gb")
			if !ok || size == 0 {
				continue
			}
			out = append(out, azDisk{
				addr:        vm + " (os_disk)",
				inline:      true,
				accountType: plan.Str(osDisk, "storage_account_type"),
				sizeGiB:     size,
			})
		}
	}
	return out
}

// azInlineDisks reads a disk block declared inside the VM resource itself,
// under whatever names that resource uses for the block and for the account
// type. Only uncached disks come back, for the same reason as everywhere else
// in this rule: a cached disk counts against a different VM limit that this
// catalog does not carry.
func azInlineDisks(g *graph.Graph, vm, block, typeKey string) []azDisk {
	var out []azDisk

	for _, values := range g.Instances(vm) {
		disks, ok := values[block].([]any)
		if !ok {
			continue
		}
		for _, item := range disks {
			disk, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if caching := plan.Str(disk, "caching"); caching != "None" {
				continue
			}
			size, ok := plan.Num(disk, "disk_size_gb")
			if !ok || size == 0 {
				continue
			}
			// The disk has no address of its own: it is part of the VM. Naming
			// the block and the disk keeps the finding pointing at something the
			// reader can find in their terraform.
			addr := vm + " (" + block
			if name := plan.Str(disk, "name"); name != "" {
				addr += " " + name
			}
			out = append(out, azDisk{
				addr:        addr + ")",
				inline:      true,
				accountType: plan.Str(disk, typeKey),
				sizeGiB:     size,
			})
		}
	}
	return out
}
