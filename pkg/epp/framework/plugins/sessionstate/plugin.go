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
	maxCandidateEndpoints = 4096
	endpointLockStripes   = 64
)

// Plugin owns one central in-memory session-state store.
type Plugin struct {
	typedName        fwkplugin.TypedName
	store            storeapi.Store
	endpointCapacity int

	endpointMu          sync.Mutex
	endpointLocks       [endpointLockStripes]sync.Mutex
	registeredEndpoints map[string]endpointRegistration
	nextGeneration      storeapi.EndpointGeneration
}

type endpointRegistration struct {
	endpoint   fwkdl.Endpoint
	generation storeapi.EndpointGeneration
}

// Provider is the consumer-facing session-state contract.
type Provider interface {
	storeapi.Store
	EndpointGeneration(endpoint string) (storeapi.EndpointGeneration, bool)
}

var (
	_ Provider                = (*Plugin)(nil)
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
	storeConfig, endpointCapacity, err := parametersConfig.values()
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
		endpointCapacity:    endpointCapacity,
		registeredEndpoints: make(map[string]endpointRegistration),
	}
	if registerer := handle.Metrics(); registerer != nil {
		if err := registerer.Register(newStoreCollector(plugin)); err != nil {
			return nil, fmt.Errorf("register session-state-store metrics: %w", err)
		}
	}
	return plugin, nil
}

// TypedName returns the configured plugin identity.
func (p *Plugin) TypedName() fwkplugin.TypedName { return p.typedName }

// Store returns the provider's lifecycle-aware store contract.
func (p *Plugin) Store() Provider { return p }

// EndpointGeneration snapshots the currently registered generation for an endpoint.
// Callers must capture it when selecting the endpoint and include it in the later estimate.
func (p *Plugin) EndpointGeneration(endpoint string) (storeapi.EndpointGeneration, bool) {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	registration, exists := p.registeredEndpoints[endpoint]
	return registration.generation, exists && registration.generation != 0
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

// RecordEstimate delegates to the owned store after validating endpoint generation.
func (p *Plugin) RecordEstimate(ctx context.Context, residency storeapi.Residency) error {
	endpointLock := p.endpointLock(residency.Endpoint)
	endpointLock.Lock()
	defer endpointLock.Unlock()

	p.endpointMu.Lock()
	registration, available := p.registeredEndpoints[residency.Endpoint]
	p.endpointMu.Unlock()
	if !available || residency.Generation == 0 ||
		residency.Generation != registration.generation {
		return storeapi.ErrEndpointUnavailable
	}
	return p.store.RecordEstimate(ctx, residency)
}

// Coverage delegates to the owned central store.
func (p *Plugin) Coverage(
	ctx context.Context,
	tip storeapi.NodeKey,
	requested storeapi.Extent,
	candidates []string,
) ([]storeapi.EndpointCoverage, error) {
	if len(candidates) > maxCandidateEndpoints {
		return nil, fmt.Errorf("%w: too many candidate endpoints", storeapi.ErrInvalidRecord)
	}
	var usedStripes [endpointLockStripes]bool
	for _, endpoint := range candidates {
		if endpoint == "" || len(endpoint) > maxEndpointBytes {
			return nil, fmt.Errorf("%w: invalid candidate endpoint", storeapi.ErrInvalidRecord)
		}
		usedStripes[p.endpointLockIndex(endpoint)] = true
	}
	for index := range usedStripes {
		if usedStripes[index] {
			p.endpointLocks[index].Lock()
		}
	}
	defer func() {
		for index := len(usedStripes) - 1; index >= 0; index-- {
			if usedStripes[index] {
				p.endpointLocks[index].Unlock()
			}
		}
	}()

	p.endpointMu.Lock()
	availableCandidates := make([]string, 0, len(candidates))
	for _, endpoint := range candidates {
		if registration, exists := p.registeredEndpoints[endpoint]; exists &&
			registration.generation != 0 {
			availableCandidates = append(availableCandidates, endpoint)
		}
	}
	p.endpointMu.Unlock()
	return p.store.Coverage(ctx, tip, requested, availableCandidates)
}

// PurgeEndpoint removes endpoint residency state without unregistering its generation.
func (p *Plugin) PurgeEndpoint(ctx context.Context, endpoint string) error {
	endpointLock := p.endpointLock(endpoint)
	endpointLock.Lock()
	defer endpointLock.Unlock()
	return p.store.PurgeEndpoint(ctx, endpoint)
}

// Stats returns bounded store cardinalities.
func (p *Plugin) Stats() storeapi.Stats { return p.store.Stats() }

func (p *Plugin) registrationCount() int {
	p.endpointMu.Lock()
	defer p.endpointMu.Unlock()
	return len(p.registeredEndpoints)
}

func (p *Plugin) endpointLock(endpoint string) *sync.Mutex {
	return &p.endpointLocks[p.endpointLockIndex(endpoint)]
}

func (p *Plugin) endpointLockIndex(endpoint string) int {
	hash := uint32(2166136261)
	for index := range len(endpoint) {
		hash ^= uint32(endpoint[index])
		hash *= 16777619
	}
	return int(hash % endpointLockStripes)
}

func (p *Plugin) nextEndpointGenerationLocked() storeapi.EndpointGeneration {
	p.nextGeneration++
	if p.nextGeneration == 0 {
		p.nextGeneration++
	}
	return p.nextGeneration
}

// DumpState emits only aggregate cardinalities.
func (p *Plugin) DumpState() (json.RawMessage, error) {
	return json.Marshal(p.Stats())
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
	endpointLock := e.provider.endpointLock(endpoint)
	endpointLock.Lock()
	defer endpointLock.Unlock()

	e.provider.endpointMu.Lock()
	switch event.Type {
	case fwkdl.EventAddOrUpdate:
		if _, exists := e.provider.registeredEndpoints[endpoint]; exists {
			generation := e.provider.nextEndpointGenerationLocked()
			e.provider.registeredEndpoints[endpoint] = endpointRegistration{
				endpoint: event.Endpoint,
			}
			e.provider.endpointMu.Unlock()
			if err := e.provider.store.PurgeEndpoint(context.WithoutCancel(ctx), endpoint); err != nil {
				e.provider.endpointMu.Lock()
				delete(e.provider.registeredEndpoints, endpoint)
				e.provider.endpointMu.Unlock()
				return err
			}
			e.provider.endpointMu.Lock()
			e.provider.registeredEndpoints[endpoint] = endpointRegistration{
				endpoint:   event.Endpoint,
				generation: generation,
			}
			e.provider.endpointMu.Unlock()
			return nil
		}
		if len(e.provider.registeredEndpoints) >= e.provider.endpointCapacity {
			e.provider.endpointMu.Unlock()
			return errors.New("session-state endpoint registration capacity exceeded")
		}
		e.provider.registeredEndpoints[endpoint] = endpointRegistration{
			endpoint:   event.Endpoint,
			generation: e.provider.nextEndpointGenerationLocked(),
		}
		e.provider.endpointMu.Unlock()
	case fwkdl.EventDelete:
		registered, exists := e.provider.registeredEndpoints[endpoint]
		if !exists || registered.endpoint != event.Endpoint {
			e.provider.endpointMu.Unlock()
			return nil
		}
		e.provider.registeredEndpoints[endpoint] = endpointRegistration{
			endpoint: event.Endpoint,
		}
		e.provider.endpointMu.Unlock()
		if err := e.provider.store.PurgeEndpoint(context.WithoutCancel(ctx), endpoint); err != nil {
			e.provider.endpointMu.Lock()
			delete(e.provider.registeredEndpoints, endpoint)
			e.provider.endpointMu.Unlock()
			return err
		}
		e.provider.endpointMu.Lock()
		delete(e.provider.registeredEndpoints, endpoint)
		e.provider.endpointMu.Unlock()
	default:
		e.provider.endpointMu.Unlock()
	}
	return nil
}
