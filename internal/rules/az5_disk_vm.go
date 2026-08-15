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
func azDiskVMAsymmetry(f *plan.File, g *graph.Graph, c *catalog.Catalog) []Finding {
	var out []Finding

	for _, vmType := range []string{"azurerm_linux_virtual_machine", "azurerm_windows_virtual_machine"} {
		for _, vm := range f.ByType(vmType) {
			addr := plan.Base(vm.Address)
			sizeName := azVMSizeOf(vm.Values)

			size, ok := c.AzureVMSizeOf(sizeName)
			if !ok {
				continue
			}

			iops, mbps := 0, 0.0
			var detail []string
			resources := []string{addr}
			counted := 0
			tierSource := ""

			for _, disk := range azUncachedDisks(g, addr, vmType) {
				tier, known := c.AzureDiskTierFor(disk.accountType, disk.sizeGiB)
				if !known {
					if disk.iops > 0 {
						// Premium SSD v2 and Ultra carry their own numbers on
						// the resource, so there is nothing to look up.
						iops += disk.iops
						mbps += float64(disk.mbps)
						counted++
						resources = append(resources, disk.addr)
						detail = append(detail, fmt.Sprintf(
							"%s (%s, %d GiB) provisions %d IOPS and %d MB/s explicitly.",
							disk.addr, disk.accountType, disk.sizeGiB, disk.iops, disk.mbps))
					}
					continue
				}
				iops += tier.IOPS
				mbps += float64(tier.MBps)
				counted++
				tierSource = tier.Source
				resources = append(resources, disk.addr)
				detail = append(detail, fmt.Sprintf(
					"%s is %d GiB of %s, which Azure bills and serves as a %s: %d IOPS and %d MB/s.",
					disk.addr, disk.sizeGiB, tier.Kind, tier.Tier, tier.IOPS, tier.MBps))
			}

			if counted == 0 {
				continue
			}

			overIOPS := size.UncachedIOPS > 0 && iops > size.UncachedIOPS
			overMBps := size.UncachedMBps > 0 && mbps > size.UncachedMBps
			if !overIOPS && !overMBps {
				continue
			}

			detail = append(detail, fmt.Sprintf(
				"%s is a %s, which drives at most %d uncached IOPS and %.0f MB/s across every uncached disk attached to it.",
				addr, size.Name, size.UncachedIOPS, size.UncachedMBps))
			if size.BurstIOPS > size.UncachedIOPS {
				detail = append(detail, fmt.Sprintf(
					"The size can burst to %d IOPS and %d MB/s for up to 30 minutes at a time, so a benchmark will not show this and a sustained workload will.",
					size.BurstIOPS, size.BurstMBps))
			}
			detail = append(detail,
				"Catalog note: "+c.AzureStorageNotes(),
				"Two ways out, and they cost differently: a larger VM size raises the cap, or a smaller disk tier stops paying for performance that cannot arrive. Which one is right depends on whether the capacity or the throughput was the point.")
			if tierSource != "" {
				// The finding quotes two documents. Source carries the VM one,
				// so the disk one has to be printed or the reader can only
				// check half the arithmetic.
				detail = append(detail, "Disk tiers: "+tierSource)
			}

			var problems []string
			if overIOPS {
				problems = append(problems, fmt.Sprintf("%d provisioned IOPS against a %d IOPS cap", iops, size.UncachedIOPS))
			}
			if overMBps {
				problems = append(problems, fmt.Sprintf("%.0f MB/s provisioned against a %.0f MB/s cap", mbps, size.UncachedMBps))
			}

			out = append(out, Finding{
				Rule:     "AZ5",
				Severity: SeverityWarning,
				Title:    "Disk provisions performance the VM is not allowed to drive",
				Summary: fmt.Sprintf(
					"%s attaches %s to a %s: %s. The disk performance above the VM cap is provisioned and billed and can never be used.",
					addr, azPlural(counted, "uncached disk", "uncached disks"), size.Name, join(problems)),
				Detail:     detail,
				Confidence: size.Confidence,
				Resources:  resources,
				Source:     size.Source,
				Metrics: map[string]int{
					"disk_iops":     iops,
					"vm_iops":       size.UncachedIOPS,
					"disk_mbps":     azRound(mbps),
					"vm_mbps":       azRound(size.UncachedMBps),
					"disks_counted": counted,
				},
			})
		}
	}
	return out
}

type azDisk struct {
	addr        string
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
				accountType: plan.Str(osDisk, "storage_account_type"),
				sizeGiB:     size,
			})
		}
	}
	return out
}
