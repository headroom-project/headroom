package rules

import (
	"fmt"

	"github.com/headroom-project/headroom/internal/catalog"
	"github.com/headroom-project/headroom/internal/graph"
	"github.com/headroom-project/headroom/internal/plan"
)

// ruleLambdaSQS checks the seam between a queue and the function draining it.
//
// Three of the four checks need no estimate at all: the numbers are in the plan
// and either agree with each other or do not. The fourth states the drain
// ceiling that an explicit concurrency cap implies, which is the number the
// person who typed the cap almost never worked out.
func ruleLambdaSQS(f *plan.File, g *graph.Graph, c *catalog.Catalog) []Finding {
	var out []Finding
	sqs := c.SQSEventSource()

	for _, esm := range f.ByType("aws_lambda_event_source_mapping") {
		addr := plan.Base(esm.Address)

		queues := g.RefsOfType(addr, "aws_sqs_queue")
		fns := g.RefsOfType(addr, "aws_lambda_function")
		if len(queues) == 0 || len(fns) == 0 {
			continue
		}
		queue, fn := queues[0], fns[0]

		fnTimeout, hasTimeout := plan.Num(g.Values(fn), "timeout")
		visibility, hasVisibility := plan.Num(g.Values(queue), "visibility_timeout_seconds")
		reserved, hasReserved := plan.Num(g.Values(fn), "reserved_concurrent_executions")

		batch, ok := plan.Num(esm.Values, "batch_size")
		if !ok || batch == 0 {
			batch = sqs.DefaultBatchSize
		}

		if hasTimeout && hasVisibility && visibility > 0 && fnTimeout > 0 {
			switch {
			case visibility < fnTimeout:
				out = append(out, Finding{
					Rule:     "R3",
					Severity: SeverityCritical,
					Title:    "Queue gives up on a message before the function finishes it",
					Summary: fmt.Sprintf(
						"%s has a visibility timeout of %ds while %s is allowed to run for %ds. A message becomes visible again while the first invocation is still working on it, so a second invocation picks it up and the work runs twice.",
						queue, visibility, fn, fnTimeout),
					Detail: []string{
						fmt.Sprintf("Visibility timeout must be at least the function timeout. AWS recommends %dx it, which would be %ds here.",
							sqs.RecommendedVisibilityMultiple, sqs.RecommendedVisibilityMultiple*fnTimeout),
						"This is not a capacity limit, it is duplicate processing that only shows up under load, when invocations start taking the full timeout.",
						sqs.VisibilityNotes,
					},
					Confidence: "high",
					Resources:  []string{queue, fn, addr},
					Source:     sqs.Source,
					Metrics: map[string]int{
						"visibility_timeout": visibility,
						"function_timeout":   fnTimeout,
						"recommended":        sqs.RecommendedVisibilityMultiple * fnTimeout,
					},
				})
			case visibility < sqs.RecommendedVisibilityMultiple*fnTimeout:
				out = append(out, Finding{
					Rule:     "R3",
					Severity: SeverityWarning,
					Title:    "Visibility timeout leaves no room for retries",
					Summary: fmt.Sprintf(
						"%s allows %ds of visibility against a %ds function timeout. It covers one attempt and nothing more, so a slow invocation followed by a retry can still double-process.",
						queue, visibility, fnTimeout),
					Detail: []string{
						fmt.Sprintf("AWS recommends %dx the function timeout, which is %ds here.",
							sqs.RecommendedVisibilityMultiple, sqs.RecommendedVisibilityMultiple*fnTimeout),
						sqs.VisibilityNotes,
					},
					Confidence: "high",
					Resources:  []string{queue, fn},
					Source:     sqs.Source,
					Metrics: map[string]int{
						"visibility_timeout": visibility,
						"function_timeout":   fnTimeout,
						"recommended":        sqs.RecommendedVisibilityMultiple * fnTimeout,
					},
				})
			}
		}

		if hasReserved && reserved > 0 && reserved < sqs.MinReservedConcurrency {
			out = append(out, Finding{
				Rule:     "R3",
				Severity: SeverityCritical,
				Title:    "Reserved concurrency starves the queue poller itself",
				Summary: fmt.Sprintf(
					"%s reserves %d concurrent executions, below the %d Lambda needs to run an SQS event source. The poller gets throttled, which shows up as messages ageing in the queue rather than as function errors.",
					fn, reserved, sqs.MinReservedConcurrency),
				Detail:     []string{sqs.ConcurrencyNotes},
				Confidence: sqs.Confidence,
				Resources:  []string{fn, addr},
				Source:     sqs.Source,
				Metrics: map[string]int{
					"reserved": reserved,
					"minimum":  sqs.MinReservedConcurrency,
				},
			})
		}

		// A concurrency cap is a throughput decision written in the wrong
		// units. Convert it so the person who typed it can see what they chose.
		concurrency, capSource := concurrencyCap(esm.Values, reserved, hasReserved)
		if concurrency > 0 && hasTimeout && fnTimeout > 0 {
			perSecond := float64(concurrency*batch) / float64(fnTimeout)
			out = append(out, Finding{
				Rule:     "R3",
				Severity: SeverityWarning,
				Title:    "Concurrency cap sets a drain rate nobody wrote down",
				Summary: fmt.Sprintf(
					"%s is capped at %d concurrent executions with a batch size of %d and a %ds timeout, so in the worst case %s drains at %.1f messages per second. Any sustained arrival rate above that grows the backlog forever.",
					fn, concurrency, batch, fnTimeout, queue, perSecond),
				Detail: []string{
					fmt.Sprintf("Cap comes from %s.", capSource),
					fmt.Sprintf("Worst case assumes every invocation uses the full %ds timeout. If real duration is shorter the drain rate is proportionally higher, and the plan does not say what it is.", fnTimeout),
					"The ceiling is a choice, not a defect. It is worth stating because the number that was typed is a concurrency, and the number that matters is a rate.",
				},
				Confidence: "medium",
				Resources:  []string{fn, queue, addr},
				Source:     sqs.Source,
				Metrics: map[string]int{
					"concurrency":        concurrency,
					"batch_size":         batch,
					"function_timeout":   fnTimeout,
					"drain_per_min_ceil": int(perSecond * 60),
				},
			})
		}
	}
	return out
}

// concurrencyCap prefers the event source mapping's own limit, since it bounds
// the poller directly, and falls back to the function's reserved concurrency.
func concurrencyCap(esmValues map[string]any, reserved int, hasReserved bool) (int, string) {
	if list, ok := esmValues["scaling_config"].([]any); ok {
		for _, item := range list {
			cfg, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if n, ok := plan.Num(cfg, "maximum_concurrency"); ok && n > 0 {
				return n, "scaling_config.maximum_concurrency on the event source mapping"
			}
		}
	}
	if hasReserved && reserved > 0 {
		return reserved, "reserved_concurrent_executions on the function"
	}
	return 0, ""
}
