package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/plan"
)

// gcpDiskSpec is one disk found anywhere in the plan, flattened out of whichever
// nested block it was declared in.
type gcpDiskSpec struct {
	owner string
	role  string
	kind  string
	gib   int
}

// gcpDiskPerformance states the IOPS a persistent disk can actually sustain,
// which is a pure function of its size and is written down nowhere.
//
// A persistent disk has no burst bucket and no baseline floor: pd-standard
// delivers 0.75 read IOPS per GiB and that is the whole story, so a 20 GiB boot
// disk sustains fifteen read IOPS forever. Nobody sizes a disk for IOPS, because
// the size field looks like it is about capacity, and 20 GiB is plainly enough
// room for an operating system. The consequence shows up later as an instance
// that is slow in a way no CPU or memory graph explains.
//
// This rule never reports critical. A small pd-standard disk can be exactly the
// right choice, and the threshold at which the number becomes worth printing is
// headroom's judgement rather than a Google limit.
func gcpDiskPerformance(f *plan.File, c *catalog.Catalog) []Finding {
	var out []Finding
	floor := c.GCPDiskReportingFloor()
	if floor.ReadIOPS == 0 {
		return nil
	}
	vcpuCap := c.GCPDiskVCPUCap()

	for _, d := range gcpDisks(f) {
		spec, known := c.GCPDisk(d.kind)
		if !known || d.gib <= 0 {
			continue
		}
		read := int(spec.ReadIOPSPerGiB * float64(d.gib))
		write := int(spec.WriteIOPSPerGiB * float64(d.gib))
		throughput := spec.ThroughputMiBPerGiB * float64(d.gib)
		if read >= floor.ReadIOPS {
			continue
		}

		detail := []string{
			fmt.Sprintf("%s is %d GiB of %s: %d read IOPS, %d write IOPS and %.1f MiB/s sustained, with no burst credits to hide behind.",
				d.owner, d.gib, d.kind, read, write, throughput),
			fmt.Sprintf("Performance is bought by the gibibyte and by nothing else, so the same %s reaches the %d read IOPS worth mentioning at %d GiB.",
				d.kind, floor.ReadIOPS, ceilDiv(floor.ReadIOPS*100, int(spec.ReadIOPSPerGiB*100))),
			spec.Notes,
		}
		if !vcpuCap.Encoded {
			detail = append(detail,
				"Treat that as an upper bound. The machine type caps disk performance a second time by vCPU count, and Google publishes those limits per machine family rather than as one table, so headroom does not encode them: the real ceiling can only be lower than the number above.")
		}
		detail = append(detail, floor.Notes)

		out = append(out, Finding{
			Rule:     "GC4",
			Severity: SeverityWarning,
			Title:    "Persistent disk is sized for capacity, not for IOPS",
			Summary: fmt.Sprintf(
				"The %s %s on %s is %d GiB, which buys %d sustained read IOPS and %.1f MiB/s. Persistent disk performance scales with size alone, so this is the ceiling whatever runs on it.",
				d.kind, d.role, d.owner, d.gib, read, throughput),
			Detail:     detail,
			Confidence: spec.Confidence,
			Resources:  []string{d.owner},
			Source:     spec.Source,
			Metrics: map[string]int{
				"size_gib":         d.gib,
				"read_iops":        read,
				"write_iops":       write,
				"throughput_kibps": int(throughput * 1024),
				"floor_read_iops":  floor.ReadIOPS,
			},
		})
	}
	return out
}

// gcpDisks flattens every persistent disk in the plan out of the four different
// shapes terraform declares them in.
func gcpDisks(f *plan.File) []gcpDiskSpec {
	var out []gcpDiskSpec

	for _, d := range f.ByType("google_compute_disk") {
		size, _ := plan.Num(d.Values, "size")
		out = append(out, gcpDiskSpec{plan.Base(d.Address), "disk", plan.Str(d.Values, "type"), size})
	}

	for _, t := range f.ByType("google_compute_instance_template") {
		for _, disk := range gcpBlocksAt(t.Values, "disk") {
			size, _ := plan.Num(disk, "disk_size_gb")
			role := "disk"
			if boot, ok := disk["boot"].(bool); ok && boot {
				role = "boot disk"
			}
			out = append(out, gcpDiskSpec{plan.Base(t.Address), role, plan.Str(disk, "disk_type"), size})
		}
	}

	for _, p := range f.ByType("google_container_node_pool") {
		cfg := gcpAt(p.Values, "node_config")
		size, _ := plan.Num(cfg, "disk_size_gb")
		out = append(out, gcpDiskSpec{plan.Base(p.Address), "node boot disk", plan.Str(cfg, "disk_type"), size})
	}

	for _, i := range f.ByType("google_compute_instance") {
		params := gcpAt(i.Values, "boot_disk", "initialize_params")
		size, _ := plan.Num(params, "size")
		out = append(out, gcpDiskSpec{plan.Base(i.Address), "boot disk", plan.Str(params, "type"), size})
	}
	return out
}
