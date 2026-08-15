package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// azDatabaseConnections compares how many connections the application tier can
// open at full scale against what the flexible server tier actually accepts.
//
// It is the Azure twin of R1, with one important difference in how the topology
// is reconstructed. On AWS a security group ingress rule is a declared network
// edge and can be trusted. Azure puts the database behind a delegated subnet or
// a private endpoint, and neither says who is allowed to talk to it, so the
// edge used here is the application naming the server: an app setting built
// from the server's name or fqdn is the app telling you it connects.
//
// That is a weaker signal than a security group, and it is deliberately the
// only one used. Inferring "same virtual network, therefore talks to" would
// manufacture consumers, and a false positive in a capacity report costs the
// whole customer.
func azDatabaseConnections(f *plan.File, g *graph.Graph, c *catalog.Catalog, opt Options) []Finding {
	var out []Finding

	for _, server := range azFlexibleServers(f) {
		addr := plan.Base(server.address)
		sku := plan.Str(server.values, "sku_name")

		limit, ok := server.lookup(c, sku)
		if !ok {
			out = append(out, Finding{
				Rule:       "AZ1",
				Severity:   SeverityInfo,
				Title:      "Connection ceiling not evaluated",
				Summary:    fmt.Sprintf("%s runs %s, which has no encoded ceiling.", addr, sku),
				Detail:     []string{fmt.Sprintf("Microsoft publishes max_connections per compute size, and %q is not in internal/catalog/data/azure-%s.json. Reporting a number here would mean interpolating between two rows of that table, which is a guess, so the rule stays silent.", sku, server.engine)},
				Confidence: "n/a",
				Resources:  []string{addr},
			})
			continue
		}

		consumers, unresolved := azDBConsumers(g, addr)

		for _, note := range unresolved {
			out = append(out, Finding{
				Rule:       "AZ1",
				Severity:   SeverityInfo,
				Title:      "Connection demand not evaluated for a consumer",
				Summary:    fmt.Sprintf("%s connects to %s, and the plan does not state how many of it there will be.", note.addr, addr),
				Detail:     []string{note.why},
				Confidence: "n/a",
				Resources:  []string{addr, note.addr},
			})
		}
		if len(consumers) == 0 {
			continue
		}

		confidence := "high"
		detail := []string{}
		resources := []string{addr}
		total := 0

		for _, consumer := range consumers {
			pool, poolHow := azPoolSizeOf(g, consumer.appAddr, opt.DefaultPoolSize)
			if poolHow == "assumed" {
				confidence = "medium"
				poolHow = "assumed, no pool setting declared on the app (override with --pool-size)"
			}
			demand := consumer.scale.scale * pool
			total += demand
			resources = append(resources, consumer.appAddr)
			if consumer.scale.addr != consumer.appAddr {
				resources = append(resources, consumer.scale.addr)
			}
			on := ""
			if consumer.scale.addr != consumer.appAddr {
				on = " on " + consumer.scale.addr
			}
			detail = append(detail, fmt.Sprintf(
				"%s scales to %d %s%s (%s) x %d connections each (%s) = %d connections",
				consumer.appAddr, consumer.scale.scale, consumer.scale.unit, on,
				consumer.scale.how, pool, poolHow, demand))
		}
		if total == 0 {
			continue
		}

		// A server parameter resource can raise or lower max_connections and
		// the plan does not have to state the value, so the catalog default
		// stops being ground truth.
		if params := g.ReferrersOfType(addr, server.parameterType); len(params) > 0 {
			confidence = "low"
			detail = append(detail, fmt.Sprintf(
				"%s attaches %s; if it overrides max_connections the ceiling below is wrong.", addr, join(params)))
		}

		ceiling := limit.MaxUserConnections
		detail = append(detail, fmt.Sprintf(
			"%s (%s, %s, %d GiB) is created with max_connections = %d, of which %d are reserved for the service, leaving %d for the application.",
			addr, sku, azPlural(limit.VCores, "vCore", "vCores"), limit.MemoryGiB,
			limit.MaxConnections, limit.MaxConnections-limit.MaxUserConnections, ceiling))
		detail = append(detail, "Catalog note: "+limit.Notes)

		ratio := float64(total) / float64(ceiling)
		metrics := map[string]int{
			"demand":          total,
			"ceiling":         ceiling,
			"max_connections": limit.MaxConnections,
			"vcores":          limit.VCores,
			"break_at_pct":    azRound(100 / ratio),
		}

		switch {
		case ratio > 1:
			out = append(out, Finding{
				Rule:     "AZ1",
				Severity: SeverityCritical,
				Title:    "Scale asymmetry: application outgrows the database",
				Summary: fmt.Sprintf(
					"At full scale the workloads in front of %s open ~%d connections against a ceiling of %d. Saturation lands at %.0f%% of the scale this plan already authorises.",
					addr, total, ceiling, 100/ratio),
				Detail:     append(detail, server.remedy),
				Confidence: confidence,
				Resources:  resources,
				Source:     limit.Source,
				Metrics:    metrics,
			})
		case ratio >= opt.WarnAt:
			out = append(out, Finding{
				Rule:     "AZ1",
				Severity: SeverityWarning,
				Title:    "Thin connection headroom",
				Summary: fmt.Sprintf(
					"Workloads in front of %s reach ~%d of %d available connections at full scale (%.0f%% of the ceiling).",
					addr, total, ceiling, ratio*100),
				Detail:     append(detail, server.remedy),
				Confidence: confidence,
				Resources:  resources,
				Source:     limit.Source,
				Metrics:    metrics,
			})
		}
	}
	return out
}

// azFlexibleServer is one managed database engine and the catalog lookup that
// goes with it.
type azFlexibleServer struct {
	address       string
	values        map[string]any
	engine        string
	parameterType string
	remedy        string
	lookup        func(*catalog.Catalog, string) (catalog.AzureDBLimit, bool)
}

func azFlexibleServers(f *plan.File) []azFlexibleServer {
	var out []azFlexibleServer

	for _, r := range f.ByType("azurerm_postgresql_flexible_server") {
		out = append(out, azFlexibleServer{
			address:       r.Address,
			values:        r.Values,
			engine:        "postgresql",
			parameterType: "azurerm_postgresql_flexible_server_configuration",
			remedy:        "Raising max_connections is the wrong lever here: Microsoft advises against it, because every connection costs memory whether it is busy or idle. The supported fix is PgBouncer in transaction mode, which the service offers built in on every tier except Burstable, or a larger compute size.",
			lookup: func(c *catalog.Catalog, sku string) (catalog.AzureDBLimit, bool) {
				return c.AzurePostgresConnections(sku)
			},
		})
	}

	for _, r := range f.ByType("azurerm_mysql_flexible_server") {
		out = append(out, azFlexibleServer{
			address:       r.Address,
			values:        r.Values,
			engine:        "mysql",
			parameterType: "azurerm_mysql_flexible_server_configuration",
			remedy:        "MySQL flexible server does allow max_connections to be raised, to roughly twice the default, so this ceiling is softer than the PostgreSQL one. It is still memory that is being spent, and a connection pooler in front of the server is the cheaper answer.",
			lookup: func(c *catalog.Catalog, sku string) (catalog.AzureDBLimit, bool) {
				return c.AzureMySQLConnections(sku)
			},
		})
	}
	return out
}

type azConsumer struct {
	appAddr string
	scale   azWorkload
}

type azUnresolved struct {
	addr string
	why  string
}

// azDBConsumers finds the workloads that name this server, and resolves how
// many of each there will be. An app on a service plan borrows the plan's
// scale; a scale set or a container app carries its own.
func azDBConsumers(g *graph.Graph, dbAddr string) ([]azConsumer, []azUnresolved) {
	var consumers []azConsumer
	var unresolved []azUnresolved
	seen := map[string]bool{}

	for _, appType := range azHostedApps {
		for _, app := range g.ReferrersOfType(dbAddr, appType) {
			if seen[app] {
				continue
			}
			seen[app] = true

			plans := g.RefsOfType(app, "azurerm_service_plan")
			if len(plans) == 0 {
				unresolved = append(unresolved, azUnresolved{app,
					"It does not reference a service plan in this plan file, so there is no declared worker count to multiply by."})
				continue
			}
			w, ok := azScaleOf(g, plans[0])
			if !ok {
				unresolved = append(unresolved, azUnresolved{app, fmt.Sprintf(
					"%s declares neither worker_count nor an autoscale setting, so the number of workers is whatever the platform defaults to.", plans[0])})
				continue
			}
			consumers = append(consumers, azConsumer{appAddr: app, scale: w})
		}
	}

	for _, wType := range azScalableWorkloads {
		if wType == "azurerm_service_plan" {
			continue
		}
		for _, workload := range g.ReferrersOfType(dbAddr, wType) {
			if seen[workload] {
				continue
			}
			seen[workload] = true
			w, ok := azScaleOf(g, workload)
			if !ok {
				unresolved = append(unresolved, azUnresolved{workload,
					"It declares no instance count, no autoscale setting and no max_replicas, so its scale is not in this plan."})
				continue
			}
			consumers = append(consumers, azConsumer{appAddr: workload, scale: w})
		}
	}

	// An AKS cluster is a real consumer and an unanswerable one: the plan
	// creates the cluster, not the Deployments on it, so the number of replicas
	// holding connections exists nowhere in this file. Saying so is worth more
	// than inventing a replica count.
	for _, cluster := range g.ReferrersOfType(dbAddr, "azurerm_kubernetes_cluster") {
		if seen[cluster] {
			continue
		}
		seen[cluster] = true
		unresolved = append(unresolved, azUnresolved{cluster,
			"It is a Kubernetes cluster: the workloads that hold the connections are Deployments applied after terraform runs, and their replica count is not in this plan. Node count and max_pods bound how many pods can exist, not how many talk to this database."})
	}

	return consumers, unresolved
}
