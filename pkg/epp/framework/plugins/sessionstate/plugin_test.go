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
	"context"
	"crypto/sha256"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	k8stypes "k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	sourcenotifications "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/source/notifications"
	storeapi "github.com/llm-d/llm-d-router/pkg/sessionstate"

	"github.com/stretchr/testify/require"
)

type fakeRegistrar struct {
	registration fwkdl.PendingRegistration
}

func (f *fakeRegistrar) Register(registration fwkdl.PendingRegistration) error {
	f.registration = registration
	return nil
}

type blockingEstimateStore struct {
	storeapi.Store
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func testNodeID(value string) storeapi.NodeID {
	return storeapi.NodeID(sha256.Sum256([]byte(value)))
}

func (s *blockingEstimateStore) RecordEstimate(
	ctx context.Context,
	residency storeapi.Residency,
) error {
	s.once.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return s.Store.RecordEstimate(ctx, residency)
	}
}

func newTestPlugin(t *testing.T, parameters string, pods fwkplugin.PodListFunc) *Plugin {
	t.Helper()
	var decoder *json.Decoder
	if parameters != "" {
		decoder = fwkplugin.StrictDecoder(json.RawMessage(parameters))
	}
	handle := fwkplugin.NewEppHandle(t.Context(), pods)
	created, err := Factory("central", decoder, handle)
	require.NoError(t, err)
	plugin, ok := created.(*Plugin)
	require.True(t, ok)
	return plugin
}

func putEstimatedNode(t *testing.T, plugin *Plugin, endpoint string) storeapi.NodeKey {
	t.Helper()
	registerTestEndpoint(t, plugin, endpoint)
	generation, found := plugin.EndpointGeneration(endpoint)
	require.True(t, found)
	nodeKey := storeapi.NodeKey{
		TenantScope: storeapi.TenantScope("scope"),
		NodeID:      testNodeID("c0"),
	}
	require.NoError(t, plugin.PutNode(t.Context(), storeapi.Node{
		Key:              nodeKey,
		CumulativeExtent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
	require.NoError(t, plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: nodeKey, Endpoint: endpoint, Generation: generation,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
	return nodeKey
}

func registerTestEndpoint(t *testing.T, plugin *Plugin, endpoint string) {
	t.Helper()
	namespace, name, found := strings.Cut(endpoint, "/")
	if !found {
		name = namespace
		namespace = ""
	}
	registered := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		ID: k8stypes.NamespacedName{Namespace: namespace, Name: name},
	}, nil)
	require.NoError(t, (&endpointExtractor{provider: plugin}).Extract(
		t.Context(),
		fwkdl.EndpointEvent{Type: fwkdl.EventAddOrUpdate, Endpoint: registered},
	))
}

func TestFactoryCreatesNamedBoundedProvider(t *testing.T) {
	plugin := newTestPlugin(t, `{
		"nodeCapacity": 2,
		"aliasCapacity": 3,
		"residencyCapacity": 4,
		"maxTipsPerEndpoint": 2,
		"maxAncestryDepth": 128,
		"maxCoverageSteps": 256,
		"nodeTTL": "30m",
		"aliasTTL": "20m",
		"estimateTTL": "90s",
		"cleanupInterval": "10m",
		"endpointReconcileInterval": "5m"
	}`, nil)

	require.Equal(t, fwkplugin.TypedName{Type: PluginType, Name: "central"}, plugin.TypedName())
	require.Same(t, plugin, plugin.Store())
	require.Equal(t, 90*time.Second, plugin.estimateTTL)
	require.Equal(t, 4, plugin.endpointCapacity)
	for _, id := range []string{"c0", "c1", "c2"} {
		require.NoError(t, plugin.PutNode(t.Context(), storeapi.Node{
			Key: storeapi.NodeKey{
				TenantScope: storeapi.TenantScope("scope"),
				NodeID:      testNodeID(id),
			},
			CumulativeExtent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
		}))
	}
	require.Equal(t, 2, plugin.Stats().Nodes)
}

func TestFactoryRejectsSecondProvider(t *testing.T) {
	handle := fwkplugin.NewEppHandle(t.Context(), nil)
	first, err := Factory("first", nil, handle)
	require.NoError(t, err)
	handle.AddPlugin("first", first)

	_, err = Factory("second", nil, handle)
	require.ErrorContains(t, err, "only one")
}

func TestFactoryRejectsUnknownAndInvalidParameters(t *testing.T) {
	handle := fwkplugin.NewEppHandle(t.Context(), nil)
	_, err := Factory("central", fwkplugin.StrictDecoder(json.RawMessage(`{"unknown": 1}`)), handle)
	require.Error(t, err)

	for _, field := range []string{
		"nodeTTL",
		"aliasTTL",
		"estimateTTL",
		"cleanupInterval",
		"endpointReconcileInterval",
	} {
		for _, value := range []string{"never", "0s", "-1s"} {
			parameters := json.RawMessage(`{"` + field + `":"` + value + `"}`)
			_, err = Factory("central", fwkplugin.StrictDecoder(parameters), handle)
			require.Error(t, err, "%s=%q should be rejected", field, value)
		}
	}

	for _, field := range []string{
		"nodeCapacity",
		"aliasCapacity",
		"residencyCapacity",
		"maxTipsPerEndpoint",
		"maxAncestryDepth",
		"maxCoverageSteps",
	} {
		for _, value := range []int{0, -1} {
			parameters := json.RawMessage(
				`{"` + field + `":` + strconv.Itoa(value) + `}`,
			)
			_, err = Factory("central", fwkplugin.StrictDecoder(parameters), handle)
			require.Error(t, err, "%s=%d should be rejected", field, value)
		}
	}
}

func TestPluginRegistersEndpointLifecycleDependency(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	registrar := &fakeRegistrar{}
	require.NoError(t, plugin.RegisterDependencies(registrar))
	require.Equal(t, plugin.TypedName(), registrar.registration.Owner)
	require.Equal(t, sourcenotifications.EndpointNotificationSourceType, registrar.registration.SourceType)
	require.NotNil(t, registrar.registration.DefaultSource)
	require.Equal(t, fwkplugin.TypedName{
		Type: sourcenotifications.EndpointNotificationSourceType,
		Name: plugin.TypedName().Name + "/endpoints",
	}, registrar.registration.DefaultSource.TypedName())
	extractor, ok := registrar.registration.Extractor.(fwkdl.EndpointExtractor)
	require.True(t, ok)
	require.Equal(t, fwkplugin.TypedName{
		Type: endpointExtractorType,
		Name: plugin.TypedName().Name + "/endpoints",
	}, extractor.TypedName())
}

func TestEndpointRegistrationsAreBoundedAndValidateIdentity(t *testing.T) {
	plugin := newTestPlugin(t, `{"residencyCapacity":1}`, nil)
	registerTestEndpoint(t, plugin, "default/pod-a")
	extractor := &endpointExtractor{provider: plugin}
	second := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		ID: k8stypes.NamespacedName{Namespace: "default", Name: "pod-b"},
	}, nil)
	err := extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type: fwkdl.EventAddOrUpdate, Endpoint: second,
	})
	require.ErrorContains(t, err, "capacity exceeded")

	oversized := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		ID: k8stypes.NamespacedName{Name: strings.Repeat("e", maxEndpointBytes+1)},
	}, nil)
	err = extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type: fwkdl.EventAddOrUpdate, Endpoint: oversized,
	})
	require.ErrorContains(t, err, "too large")

	empty := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{}, nil)
	err = extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type: fwkdl.EventAddOrUpdate, Endpoint: empty,
	})
	require.ErrorContains(t, err, "empty")
}

func TestEndpointDeletePurgesResidency(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	endpointID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-a"}
	putEstimatedNode(t, plugin, endpointID.String())
	require.Equal(t, 1, plugin.Stats().Residencies)

	extractor := &endpointExtractor{provider: plugin}
	endpoint := plugin.registeredEndpoints[endpointID.String()].endpoint
	require.NoError(t, extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: endpoint,
	}))
	require.Zero(t, plugin.Stats().Residencies)
}

func TestPublicPurgeKeepsLiveEndpointGenerationAvailable(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	endpoint := "default/pod-a"
	nodeKey := putEstimatedNode(t, plugin, endpoint)
	generation, found := plugin.EndpointGeneration(endpoint)
	require.True(t, found)

	require.NoError(t, plugin.PurgeEndpoint(t.Context(), endpoint))
	require.Zero(t, plugin.Stats().Residencies)
	currentGeneration, found := plugin.EndpointGeneration(endpoint)
	require.True(t, found)
	require.Equal(t, generation, currentGeneration)
	require.NoError(t, plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: nodeKey, Endpoint: endpoint, Generation: generation,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
}

func TestStaleEndpointDeleteDoesNotPurgeReplacement(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	endpointID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-a"}
	putEstimatedNode(t, plugin, endpointID.String())
	extractor := &endpointExtractor{provider: plugin}
	oldRegistration := plugin.registeredEndpoints[endpointID.String()]
	oldEndpoint := oldRegistration.endpoint
	replacement := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{ID: endpointID}, nil)

	require.NoError(t, extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: replacement,
	}))
	require.Zero(t, plugin.Stats().Residencies)
	newGeneration, found := plugin.EndpointGeneration(endpointID.String())
	require.True(t, found)
	require.NotEqual(t, oldRegistration.generation, newGeneration)
	err := plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: storeapi.NodeKey{
			TenantScope: storeapi.TenantScope("scope"),
			NodeID:      testNodeID("c0"),
		},
		Endpoint: endpointID.String(), Generation: oldRegistration.generation,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, storeapi.ErrEndpointUnavailable)
	require.NoError(t, plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: storeapi.NodeKey{
			TenantScope: storeapi.TenantScope("scope"),
			NodeID:      testNodeID("c0"),
		},
		Endpoint: endpointID.String(), Generation: newGeneration,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
	require.NoError(t, extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: oldEndpoint,
	}))
	require.Equal(t, 1, plugin.Stats().Residencies)

	require.NoError(t, extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: replacement,
	}))
	require.Zero(t, plugin.Stats().Residencies)
}

func TestEndpointDeleteSerializesWithEstimateAndPreventsResurrection(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	endpointID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-a"}
	endpointName := endpointID.String()
	nodeKey := storeapi.NodeKey{
		TenantScope: storeapi.TenantScope("scope"),
		NodeID:      testNodeID("c0"),
	}
	require.NoError(t, plugin.PutNode(t.Context(), storeapi.Node{
		Key:              nodeKey,
		CumulativeExtent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
	blocking := &blockingEstimateStore{
		Store:   plugin.store,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	plugin.store = blocking
	extractor := &endpointExtractor{provider: plugin}
	endpoint := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{ID: endpointID}, nil)
	require.NoError(t, extractor.Extract(t.Context(), fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: endpoint,
	}))
	generation, found := plugin.EndpointGeneration(endpointName)
	require.True(t, found)

	recordErr := make(chan error, 1)
	go func() {
		recordErr <- plugin.RecordEstimate(t.Context(), storeapi.Residency{
			Node: nodeKey, Endpoint: endpointName, Generation: generation,
			Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
		})
	}()
	<-blocking.started
	deleteErr := make(chan error, 1)
	deleteStarted := make(chan struct{})
	go func() {
		close(deleteStarted)
		deleteErr <- extractor.Extract(t.Context(), fwkdl.EndpointEvent{
			Type:     fwkdl.EventDelete,
			Endpoint: endpoint,
		})
	}()
	<-deleteStarted
	select {
	case err := <-deleteErr:
		t.Fatalf("delete completed before the in-flight estimate was released: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(blocking.release)
	require.NoError(t, <-recordErr)
	require.NoError(t, <-deleteErr)

	coverage, err := plugin.Coverage(t.Context(), nodeKey,
		storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
		[]string{endpointName})
	require.NoError(t, err)
	require.Empty(t, coverage)
	err = plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: nodeKey, Endpoint: endpointName, Generation: generation,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, storeapi.ErrEndpointUnavailable)
}

func TestEndpointReconcilePurgesMissingObservedEndpoint(t *testing.T) {
	activeID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-b"}
	plugin := newTestPlugin(t, "", func() []k8stypes.NamespacedName {
		return []k8stypes.NamespacedName{activeID}
	})
	staleID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-a"}
	nodeKey := putEstimatedNode(t, plugin, staleID.String())
	registerTestEndpoint(t, plugin, activeID.String())
	activeGeneration, found := plugin.EndpointGeneration(activeID.String())
	require.True(t, found)
	require.NoError(t, plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: nodeKey, Endpoint: activeID.String(), Generation: activeGeneration,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	}))
	plugin.reconcileEndpoints(context.Background())
	coverage, err := plugin.Coverage(t.Context(), nodeKey,
		storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
		[]string{staleID.String(), activeID.String()})
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	require.Equal(t, activeID.String(), coverage[0].Endpoint)
}

func TestEndpointReconcileDoesNotReviveDeletedGenerationFromPodList(t *testing.T) {
	endpointID := k8stypes.NamespacedName{Namespace: "default", Name: "pod-a"}
	plugin := newTestPlugin(t, "", func() []k8stypes.NamespacedName {
		return []k8stypes.NamespacedName{endpointID}
	})
	nodeKey := putEstimatedNode(t, plugin, endpointID.String())
	generation, found := plugin.EndpointGeneration(endpointID.String())
	require.True(t, found)
	endpoint := plugin.registeredEndpoints[endpointID.String()].endpoint
	require.NoError(t, (&endpointExtractor{provider: plugin}).Extract(
		t.Context(),
		fwkdl.EndpointEvent{Type: fwkdl.EventDelete, Endpoint: endpoint},
	))

	plugin.reconcileEndpoints(context.Background())
	_, found = plugin.EndpointGeneration(endpointID.String())
	require.False(t, found)
	err := plugin.RecordEstimate(t.Context(), storeapi.Residency{
		Node: nodeKey, Endpoint: endpointID.String(), Generation: generation,
		Extent: storeapi.Extent{Value: 10, Unit: storeapi.ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, storeapi.ErrEndpointUnavailable)
}

func TestDumpStateContainsCountsNotIdentifiers(t *testing.T) {
	plugin := newTestPlugin(t, "", nil)
	putEstimatedNode(t, plugin, "secret-tenant/pod-a")

	dump, err := plugin.DumpState()
	require.NoError(t, err)
	require.JSONEq(t, `{"nodes":1,"aliases":0,"residencies":1,"endpoints":1}`, string(dump))
	require.False(t, strings.Contains(string(dump), "secret-tenant"))
}

func TestMetricsExposeOnlyAggregateKinds(t *testing.T) {
	registry := prometheus.NewRegistry()
	handle := fwkplugin.NewEppHandle(t.Context(), nil, fwkplugin.WithMetricsRecorder(registry))
	created, err := Factory("central", nil, handle)
	require.NoError(t, err)
	plugin := created.(*Plugin)
	putEstimatedNode(t, plugin, "secret-tenant/pod-a")

	families, err := registry.Gather()
	require.NoError(t, err)
	require.Len(t, families, 1)
	require.Equal(t, "llm_d_epp_session_state_entries", families[0].GetName())
	require.Len(t, families[0].Metric, 4)
	valuesByKind := make(map[string]float64, len(families[0].Metric))
	for _, metric := range families[0].Metric {
		labels := make(map[string]string, len(metric.Label))
		for _, label := range metric.Label {
			labels[label.GetName()] = label.GetValue()
		}
		require.Equal(t, PluginType, labels["plugin_type"])
		require.Equal(t, "central", labels["plugin_name"])
		require.Contains(t, []string{"node", "alias", "residency", "endpoint"}, labels["kind"])
		require.NotNil(t, metric.Gauge)
		valuesByKind[labels["kind"]] = metric.GetGauge().GetValue()
	}
	require.Equal(t, map[string]float64{
		"node": 1, "alias": 0, "residency": 1, "endpoint": 1,
	}, valuesByKind)
	serialized, err := json.Marshal(families)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "secret-tenant")
}
