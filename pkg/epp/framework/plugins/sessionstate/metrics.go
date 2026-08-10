/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sessionstate

import (
	"github.com/prometheus/client_golang/prometheus"
	compbasemetrics "k8s.io/component-base/metrics"

	metricsutil "github.com/llm-d/llm-d-router/pkg/common/observability/metrics"
	eppmetrics "github.com/llm-d/llm-d-router/pkg/epp/metrics"
)

type storeCollector struct {
	provider    *Plugin
	entriesDesc *prometheus.Desc
}

func newStoreCollector(provider *Plugin) *storeCollector {
	return &storeCollector{
		provider: provider,
		entriesDesc: prometheus.NewDesc(
			prometheus.BuildFQName(
				"",
				eppmetrics.LLMDRouterEndpointPickerSubsystem,
				"session_state_entries",
			),
			metricsutil.HelpMsgWithStability(
				"Current bounded central session-state entries by kind.",
				compbasemetrics.ALPHA,
			),
			[]string{"plugin_type", "plugin_name", "kind"},
			nil,
		),
	}
}

func (c *storeCollector) Describe(descriptions chan<- *prometheus.Desc) {
	descriptions <- c.entriesDesc
}

func (c *storeCollector) Collect(metrics chan<- prometheus.Metric) {
	stats := c.provider.Stats()
	for kind, value := range map[string]int{
		"node":      stats.Nodes,
		"alias":     stats.Aliases,
		"residency": stats.Residencies,
		"endpoint":  stats.Endpoints,
	} {
		metrics <- prometheus.MustNewConstMetric(
			c.entriesDesc,
			prometheus.GaugeValue,
			float64(value),
			PluginType,
			c.provider.typedName.Name,
			kind,
		)
	}
}
