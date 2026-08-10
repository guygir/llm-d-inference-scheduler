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

// Package sessionstate exposes the shared session-state store as a named
// framework plugin so identity, affinity, and future event consumers can use
// one consistent state owner.
package sessionstate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	sourcenotifications "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/source/notifications"
	storeapi "github.com/llm-d/llm-d-router/pkg/sessionstate"
)

const (
	// PluginType is the central session-state provider plugin type.
	PluginType            = "session-state-store"
	endpointExtractorType = "session-state-endpoint-extractor"
	maxEndpointBytes      = 1024
)

// Plugin owns one central in-memory session-state store.
type Plugin struct {
	typedName        fwkplugin.TypedName
	store            storeapi.Store
	handle           fwkplugin.Handle
	estimateTTL      time.Duration
	endpointCapacity int

	endpointMu          sync.Mutex
	observedEndpoints   map[string]time.Time
	registeredEndpoints map[string]endpointRegistration
	nextGeneration      storeapi.EndpointGeneration
}

type endpointRegistration struct {
	endpoint   fwkdl.Endpoint
	generation storeapi.EndpointGeneration
}

var (
	_ storeapi.Store          = (*Plugin)(nil)
	_ fwkplugin.StateDumper   = (*Plugin)(nil)
	_ fwkdl.Registrant        = (*Plugin)(nil)
	_ fwkdl.EndpointExtractor = (*endpointExtractor)(nil)
)

// Factory creates a named central session-state provider.
func Factory(name string, parameters *json.Decoder, handle fwkplugin.Handle) (fwkplugin.Plugin, error) {
	if handle == nil {
		return nil, errors.New("session-state-store requires a plugin handle")
	}
	if name == "" {
		name = PluginType
	}
	for _, existing := range handle.GetAllPlugins() {
		if existing.TypedName().Type == PluginType {
			return nil, errors.New("only one session-state-store provider may be configured")
		}
	}

	parametersConfig := config{}
	if parameters != nil {
		if err := parameters.Decode(&parametersConfig); err != nil {
			return nil, fmt.Errorf("decode session-state-store parameters: %w", err)
		}
	}
	storeConfig, reconcileInterval, err := parametersConfig.storeConfig()
	if err != nil {
		return nil, fmt.Errorf("configure session-state-store: %w", err)
	}
	store, err := storeapi.NewMemoryStore(handle.Context(), storeConfig)
	if err != nil {
		return nil, fmt.Errorf("create session-state-store: %w", err)
	}
	plugin := &Plugin{
		typedName:           fwkplugin.TypedName{Type: PluginType, Name: name},
		store:               store,
		handle:              handle,
		estimateTTL:         storeConfig.EstimateTTL,
		endpointCapacity:    storeConfig.ResidencyCapacity,
		observedEndpoints:   make(map[string]time.Time),
		registeredEndpoints: make(map[string]endpointRegistration),
	}
	if registerer := handle.Metrics(); registerer != nil {
		if err := registerer.Register(newStoreCollector(plugin)); err != nil {
			return nil, fmt.Errorf("register session-state-store metrics: %w", err)
		}
	}
	go plugin.runEndpointReconcile(handle.Context(), reconcileInterval)
	return plugin, nil
}

// TypedName returns the configured plugin identity.
func (p *Plugin) TypedName() fwkplugin.TypedName { return p.typedName }

// Store returns the provider's lifecycle-aware store contract.
func (p *Plugin) Store() storeapi.Store { return p }

// EndpointGeneration snapshots the currently registered generation for an endpoint.
// Callers must capture it when selecting the endpoint and include it in the later estimate.
func (p *Plugin) EndpointGeneration(endpoint string) (storeapi.EndpointGeneration, bool) {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	registration, exists := p.registeredEndpoints[endpoint]
	return registration.generation, exists
}

// RegisterDependencies requests framework-owned endpoint lifecycle events.
func (p *Plugin) RegisterDependencies(registrar fwkdl.Registrar) error {
	extractor := &endpointExtractor{provider: p}
	return registrar.Register(fwkdl.PendingRegistration{
		Owner:      p.typedName,
		SourceType: sourcenotifications.EndpointNotificationSourceType,
		Extractor:  extractor,
		DefaultSource: sourcenotifications.NewEndpointDataSource(
			sourcenotifications.EndpointNotificationSourceType,
			p.typedName.Name+"/endpoints",
		),
	})
}

// PutNode delegates to the owned central store.
func (p *Plugin) PutNode(ctx context.Context, node storeapi.Node) error {
	return p.store.PutNode(ctx, node)
}

// PutAlias delegates to the owned central store.
func (p *Plugin) PutAlias(
	ctx context.Context,
	key storeapi.AliasKey,
	target storeapi.AliasTarget,
	ttl time.Duration,
) error {
	return p.store.PutAlias(ctx, key, target, ttl)
}

// ResolveAlias delegates to the owned central store.
func (p *Plugin) ResolveAlias(ctx context.Context, key storeapi.AliasKey) (storeapi.AliasTarget, bool) {
	return p.store.ResolveAlias(ctx, key)
}

// RecordEstimate delegates to the owned store and tracks the endpoint for
// lifecycle reconciliation.
func (p *Plugin) RecordEstimate(ctx context.Context, residency storeapi.Residency) error {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	registration, available := p.registeredEndpoints[residency.Endpoint]
	if !available || residency.Generation == 0 ||
		residency.Generation != registration.generation {
		return storeapi.ErrEndpointUnavailable
	}
	if err := p.store.RecordEstimate(ctx, residency); err != nil {
		return err
	}
	p.observedEndpoints[residency.Endpoint] = time.Now()
	return nil
}

// Coverage delegates to the owned central store.
func (p *Plugin) Coverage(
	ctx context.Context,
	tip storeapi.NodeKey,
	requested storeapi.Extent,
	candidates []string,
) ([]storeapi.EndpointCoverage, error) {
	return p.store.Coverage(ctx, tip, requested, candidates)
}

// PurgeEndpoint removes endpoint state from the store and reconcile set.
func (p *Plugin) PurgeEndpoint(ctx context.Context, endpoint string) error {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	return p.purgeEndpointLocked(ctx, endpoint)
}

func (p *Plugin) purgeEndpointLocked(ctx context.Context, endpoint string) error {
	if err := p.store.PurgeEndpoint(ctx, endpoint); err != nil {
		return err
	}
	delete(p.observedEndpoints, endpoint)
	return nil
}

// Expire delegates explicit expiry to the owned central store.
func (p *Plugin) Expire(now time.Time) { p.store.Expire(now) }

// Stats returns bounded store cardinalities.
func (p *Plugin) Stats() storeapi.Stats { return p.store.Stats() }

// DumpState emits only aggregate cardinalities.
func (p *Plugin) DumpState() (json.RawMessage, error) {
	return json.Marshal(p.Stats())
}

func (p *Plugin) runEndpointReconcile(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.reconcileEndpoints(ctx)
		}
	}
}

func (p *Plugin) reconcileEndpoints(ctx context.Context) {
	activeList := p.handle.PodList()
	active := make(map[string]struct{}, len(activeList))
	for _, endpoint := range activeList {
		active[endpoint.String()] = struct{}{}
	}

	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	now := time.Now()
	if activeList != nil {
		for endpoint := range p.registeredEndpoints {
			if _, isActive := active[endpoint]; !isActive {
				delete(p.registeredEndpoints, endpoint)
				_ = p.purgeEndpointLocked(ctx, endpoint)
			}
		}
	}
	for endpoint, observedAt := range p.observedEndpoints {
		_, isActive := active[endpoint]
		if activeList != nil && !isActive {
			_ = p.purgeEndpointLocked(ctx, endpoint)
			continue
		}
		if now.Sub(observedAt) > p.estimateTTL {
			delete(p.observedEndpoints, endpoint)
		}
	}
}

type endpointExtractor struct {
	provider *Plugin
}

func (e *endpointExtractor) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{
		Type: endpointExtractorType,
		Name: e.provider.typedName.Name + "/endpoints",
	}
}

func (e *endpointExtractor) Extract(ctx context.Context, event fwkdl.EndpointEvent) error {
	if event.Endpoint == nil || event.Endpoint.GetMetadata() == nil {
		return nil
	}
	endpointID := event.Endpoint.GetMetadata().GetID()
	endpoint := endpointID.String()
	if endpointID.Name == "" || len(endpoint) > maxEndpointBytes {
		return errors.New("session-state endpoint identity is empty or too large")
	}
	e.provider.endpointMu.Lock()
	defer e.provider.endpointMu.Unlock()
	switch event.Type {
	case fwkdl.EventAddOrUpdate:
		if current, exists := e.provider.registeredEndpoints[endpoint]; exists {
			if current.endpoint == event.Endpoint {
				return nil
			}
			if err := e.provider.purgeEndpointLocked(ctx, endpoint); err != nil {
				return err
			}
		} else if len(e.provider.registeredEndpoints) >= e.provider.endpointCapacity {
			return errors.New("session-state endpoint registration capacity exceeded")
		}
		e.provider.nextGeneration++
		if e.provider.nextGeneration == 0 {
			e.provider.nextGeneration++
		}
		e.provider.registeredEndpoints[endpoint] = endpointRegistration{
			endpoint:   event.Endpoint,
			generation: e.provider.nextGeneration,
		}
	case fwkdl.EventDelete:
		if registered, exists := e.provider.registeredEndpoints[endpoint]; exists &&
			registered.endpoint != event.Endpoint {
			return nil
		}
		delete(e.provider.registeredEndpoints, endpoint)
		return e.provider.purgeEndpointLocked(ctx, endpoint)
	}
	return nil
}
