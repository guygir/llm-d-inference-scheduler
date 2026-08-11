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
	"bytes"
	"container/heap"
	"container/list"
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"time"
)

const (
	defaultEstimateTier   = "GPU"
	maxTenantScopeBytes   = 4 * 1024
	maxAliasValueBytes    = 4 * 1024
	maxSessionIDBytes     = 4 * 1024
	maxEndpointBytes      = 1024
	maxCandidateEndpoints = 4096
	maxTierBytes          = 128
	expiryBatchSize       = 4096
	cleanupBatchSize      = 256
)

type nodeEntry struct {
	node      Node
	depth     int
	expiresAt time.Time
	element   *list.Element
	expiry    *expiryItem
}

type aliasEntry struct {
	target    AliasTarget
	ttl       time.Duration
	expiresAt time.Time
	element   *list.Element
	expiry    *expiryItem
}

type residencyKey struct {
	Node     NodeKey
	Endpoint string
	Tier     string
}

type residencyEntry struct {
	residency Residency
	element   *list.Element
	expiry    *expiryItem
}

type expiryKind uint8

const (
	nodeExpiry expiryKind = iota
	aliasExpiry
	residencyExpiry
)

type expiryItem struct {
	kind      expiryKind
	due       time.Time
	node      NodeKey
	alias     AliasKey
	residency residencyKey
	index     int
}

type expiryHeap []*expiryItem

func (h expiryHeap) Len() int           { return len(h) }
func (h expiryHeap) Less(i, j int) bool { return h[i].due.Before(h[j].due) }
func (h expiryHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *expiryHeap) Push(value any) {
	item := value.(*expiryItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *expiryHeap) Pop() any {
	old := *h
	last := len(old) - 1
	value := old[last]
	old[last] = nil
	*h = old[:last]
	value.index = -1
	return value
}

// MemoryStore is a bounded, concurrency-safe in-memory Store.
type MemoryStore struct {
	mu             sync.Mutex
	nodeCapacityMu sync.Mutex
	config         Config
	clock          clock

	nodes    map[NodeKey]*nodeEntry
	children map[NodeKey]map[NodeKey]struct{}
	nodeLRU  *list.List

	aliases       map[AliasKey]*aliasEntry
	aliasesByNode map[NodeKey]map[AliasKey]struct{}
	aliasLRU      *list.List

	residencies           map[residencyKey]*residencyEntry
	residenciesByNode     map[NodeKey]map[residencyKey]struct{}
	residenciesByEndpoint map[string]map[residencyKey]struct{}
	endpointTips          map[TenantScope]map[string]map[string]map[NodeKey]residencyKey
	residencyLRU          *list.List
	expiries              expiryHeap
}

type memoryStoreOption func(*MemoryStore)

func withClock(c clock) memoryStoreOption {
	return func(store *MemoryStore) {
		store.clock = c
	}
}

// NewMemoryStore creates a bounded store and starts its expiry loop.
func NewMemoryStore(ctx context.Context, config Config) (*MemoryStore, error) {
	return newMemoryStore(ctx, config)
}

func newMemoryStore(
	ctx context.Context,
	config Config,
	options ...memoryStoreOption,
) (*MemoryStore, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if ctx == nil {
		return nil, fmt.Errorf("%w: context is required", ErrInvalidConfig)
	}

	store := &MemoryStore{
		config:                config,
		clock:                 realClock{},
		nodes:                 make(map[NodeKey]*nodeEntry),
		children:              make(map[NodeKey]map[NodeKey]struct{}),
		nodeLRU:               list.New(),
		aliases:               make(map[AliasKey]*aliasEntry),
		aliasesByNode:         make(map[NodeKey]map[AliasKey]struct{}),
		aliasLRU:              list.New(),
		residencies:           make(map[residencyKey]*residencyEntry),
		residenciesByNode:     make(map[NodeKey]map[residencyKey]struct{}),
		residenciesByEndpoint: make(map[string]map[residencyKey]struct{}),
		endpointTips:          make(map[TenantScope]map[string]map[string]map[NodeKey]residencyKey),
		residencyLRU:          list.New(),
	}
	for _, option := range options {
		option(store)
	}
	if store.clock == nil {
		return nil, fmt.Errorf("%w: clock is required", ErrInvalidConfig)
	}

	go store.runExpiry(ctx)
	return store, nil
}

func (s *MemoryStore) runExpiry(ctx context.Context) {
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.expireCooperatively(ctx, s.clock.Now())
		}
	}
}

func (s *MemoryStore) expireCooperatively(ctx context.Context, now time.Time) {
	for {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		more := s.expireBatchLocked(now, expiryBatchSize)
		s.mu.Unlock()
		if !more {
			return
		}
		runtime.Gosched()
	}
}

// PutNode inserts or refreshes one immutable lineage node.
func (s *MemoryStore) PutNode(ctx context.Context, child Node) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateNodeKey(child.Key); err != nil {
		return err
	}
	if err := validateExtentUnit(child.CumulativeExtent.Unit); err != nil {
		return err
	}
	child.Parent = cloneNodeKey(child.Parent)

	s.nodeCapacityMu.Lock()
	defer s.nodeCapacityMu.Unlock()
	s.mu.Lock()
	locked := true
	defer func() {
		if locked {
			s.mu.Unlock()
		}
	}()
	now := s.clock.Now()
	s.removeNodeIfExpiredLocked(child.Key, now)
	if existing, ok := s.nodes[child.Key]; ok {
		if !nodesEqual(existing.node, child) {
			return ErrNodeConflict
		}
		s.touchNodeLocked(existing, now)
		return nil
	}
	depth := 1

	if child.Parent != nil {
		s.removeNodeIfExpiredLocked(*child.Parent, now)
		if child.Parent.TenantScope != child.Key.TenantScope {
			return fmt.Errorf("%w: parent and child tenant scopes differ", ErrInvalidNode)
		}
		parentEntry, ok := s.nodes[*child.Parent]
		if !ok {
			return fmt.Errorf("%w: parent %x", ErrNodeNotFound, child.Parent.NodeID)
		}
		if parentEntry.node.CumulativeExtent.Unit != child.CumulativeExtent.Unit {
			return fmt.Errorf("%w: parent=%s child=%s", ErrExtentUnit,
				parentEntry.node.CumulativeExtent.Unit, child.CumulativeExtent.Unit)
		}
		if child.CumulativeExtent.Value < parentEntry.node.CumulativeExtent.Value {
			return fmt.Errorf("%w: child extent is smaller than parent extent", ErrInvalidNode)
		}
		depth = parentEntry.depth + 1
		if depth > s.config.MaxAncestryDepth {
			return ErrAncestryTooDeep
		}
		s.touchNodeLocked(parentEntry, now)
	}

	entry := &nodeEntry{node: child, depth: depth, expiresAt: now.Add(s.config.NodeTTL)}
	entry.element = s.nodeLRU.PushFront(child.Key)
	s.nodes[child.Key] = entry
	s.scheduleNodeExpiryLocked(child.Key, entry.expiresAt)
	if child.Parent != nil {
		if s.children[*child.Parent] == nil {
			s.children[*child.Parent] = make(map[NodeKey]struct{})
		}
		s.children[*child.Parent][child.Key] = struct{}{}
	}
	s.mu.Unlock()
	locked = false
	s.enforceNodeCapacity()
	return nil
}

// PutAlias binds or rebinds one typed external identifier.
func (s *MemoryStore) PutAlias(ctx context.Context, key AliasKey, target AliasTarget, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateAliasKey(key); err != nil {
		return err
	}
	if err := validateNodeKey(target.Node); err != nil {
		return fmt.Errorf("%w: invalid target node", ErrInvalidAlias)
	}
	if key.TenantScope != target.Node.TenantScope {
		return fmt.Errorf("%w: alias and target tenant scopes differ", ErrInvalidAlias)
	}
	if target.Session != nil && target.Session.TenantScope != key.TenantScope {
		return fmt.Errorf("%w: alias and session tenant scopes differ", ErrInvalidAlias)
	}
	if target.Session != nil &&
		(target.Session.SessionID == "" || len(target.Session.SessionID) > maxSessionIDBytes) {
		return fmt.Errorf("%w: session ID is empty or too large", ErrInvalidAlias)
	}
	if ttl <= 0 || ttl > s.config.AliasTTL {
		ttl = s.config.AliasTTL
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()
	s.removeNodeIfExpiredLocked(target.Node, now)
	node, ok := s.nodes[target.Node]
	if !ok {
		return fmt.Errorf("%w: alias target %x", ErrNodeNotFound, target.Node.NodeID)
	}
	s.touchNodeLocked(node, now)

	if existing := s.aliases[key]; existing != nil && !existing.expiresAt.After(now) {
		s.removeAliasLocked(key)
	}
	target = cloneAliasTarget(target)
	if existing, ok := s.aliases[key]; ok {
		s.unlinkAliasFromNodeLocked(key, existing.target.Node)
		existing.target = target
		existing.ttl = ttl
		existing.expiresAt = now.Add(ttl)
		s.aliasLRU.MoveToFront(existing.element)
		s.scheduleAliasExpiryLocked(key, existing.expiresAt)
		s.linkAliasToNodeLocked(key, target.Node)
		return nil
	}

	entry := &aliasEntry{target: target, ttl: ttl, expiresAt: now.Add(ttl)}
	entry.element = s.aliasLRU.PushFront(key)
	s.aliases[key] = entry
	s.scheduleAliasExpiryLocked(key, entry.expiresAt)
	s.linkAliasToNodeLocked(key, target.Node)
	for len(s.aliases) > s.config.AliasCapacity {
		s.evictOldestAliasLocked()
	}
	return nil
}

// ResolveAlias returns and refreshes one non-expired alias.
func (s *MemoryStore) ResolveAlias(ctx context.Context, key AliasKey) (AliasTarget, bool) {
	if ctx == nil || ctx.Err() != nil {
		return AliasTarget{}, false
	}
	if validateAliasKey(key) != nil {
		return AliasTarget{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()

	entry, ok := s.aliases[key]
	if !ok {
		return AliasTarget{}, false
	}
	if !entry.expiresAt.After(now) {
		s.removeAliasLocked(key)
		return AliasTarget{}, false
	}
	s.removeNodeIfExpiredLocked(entry.target.Node, now)
	node, nodeExists := s.nodes[entry.target.Node]
	if !nodeExists {
		s.removeAliasLocked(key)
		return AliasTarget{}, false
	}
	entry.expiresAt = now.Add(entry.ttl)
	s.aliasLRU.MoveToFront(entry.element)
	s.scheduleAliasExpiryLocked(key, entry.expiresAt)
	s.touchNodeLocked(node, now)
	return cloneAliasTarget(entry.target), true
}

// RecordEstimate records one bounded maximal endpoint tip.
func (s *MemoryStore) RecordEstimate(ctx context.Context, residency Residency) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateResidency(residency); err != nil {
		return err
	}
	if residency.Tier == "" {
		residency.Tier = defaultEstimateTier
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()

	s.removeNodeIfExpiredLocked(residency.Node, now)
	node, ok := s.nodes[residency.Node]
	if !ok {
		return fmt.Errorf("%w: residency target %x", ErrNodeNotFound, residency.Node.NodeID)
	}
	if node.node.CumulativeExtent.Unit != residency.Extent.Unit {
		return fmt.Errorf("%w: node=%s residency=%s", ErrExtentUnit,
			node.node.CumulativeExtent.Unit, residency.Extent.Unit)
	}
	if residency.Extent.Value > node.node.CumulativeExtent.Value {
		return fmt.Errorf("%w: residency extent exceeds node extent", ErrInvalidRecord)
	}
	residency.ObservedAt = now
	residency.ExpiresAt = now.Add(s.config.EstimateTTL)
	s.touchNodeLocked(node, now)

	key := residencyKey{Node: residency.Node, Endpoint: residency.Endpoint, Tier: residency.Tier}
	if existing := s.residencies[key]; existing != nil &&
		!existing.residency.ExpiresAt.After(now) {
		s.removeResidencyLocked(key)
	}
	if existing, exists := s.residencies[key]; exists {
		existing.residency = residency
		s.residencyLRU.MoveToFront(existing.element)
		s.scheduleResidencyExpiryLocked(key, residency.ExpiresAt)
	} else {
		entry := &residencyEntry{residency: residency}
		entry.element = s.residencyLRU.PushFront(key)
		s.residencies[key] = entry
		s.linkResidencyToNodeLocked(key)
		s.scheduleResidencyExpiryLocked(key, residency.ExpiresAt)
	}

	scopeTips := s.endpointTips[residency.Node.TenantScope]
	if scopeTips == nil {
		scopeTips = make(map[string]map[string]map[NodeKey]residencyKey)
		s.endpointTips[residency.Node.TenantScope] = scopeTips
	}
	if scopeTips[residency.Endpoint] == nil {
		scopeTips[residency.Endpoint] = make(map[string]map[NodeKey]residencyKey)
	}
	if scopeTips[residency.Endpoint][residency.Tier] == nil {
		scopeTips[residency.Endpoint][residency.Tier] = make(map[NodeKey]residencyKey)
	}
	scopeTips[residency.Endpoint][residency.Tier][residency.Node] = key
	if err := s.compactEndpointTipsLocked(
		residency.Node.TenantScope, residency.Endpoint, residency.Tier, key,
	); err != nil {
		return err
	}
	for len(s.residencies) > s.config.ResidencyCapacity {
		s.evictOldestResidencyLocked()
	}
	return nil
}

// Coverage returns the deepest compatible covered extent for each candidate endpoint and tier.
func (s *MemoryStore) Coverage(
	ctx context.Context,
	tip NodeKey,
	requested Extent,
	candidates []string,
) ([]EndpointCoverage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateNodeKey(tip); err != nil {
		return nil, err
	}
	if err := validateExtentUnit(requested.Unit); err != nil {
		return nil, err
	}
	if len(candidates) > maxCandidateEndpoints {
		return nil, fmt.Errorf("%w: too many candidate endpoints", ErrInvalidRecord)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clock.Now()

	s.removeNodeIfExpiredLocked(tip, now)
	tipEntry, ok := s.nodes[tip]
	if !ok {
		return nil, fmt.Errorf("%w: requested tip %x", ErrNodeNotFound, tip.NodeID)
	}
	if tipEntry.node.CumulativeExtent.Unit != requested.Unit {
		return nil, fmt.Errorf("%w: tip=%s requested=%s", ErrExtentUnit,
			tipEntry.node.CumulativeExtent.Unit, requested.Unit)
	}
	s.touchNodeLocked(tipEntry, now)

	coverageSteps := 0
	incomingAncestors, err := s.ancestorSetLocked(ctx, tip, now, &coverageSteps)
	if err != nil {
		return nil, err
	}
	byEndpointTier := make(map[endpointTierKey]coverageChoice, len(candidates))
	seenCandidates := make(map[string]struct{}, len(candidates))
	for _, endpoint := range candidates {
		if endpoint == "" || len(endpoint) > maxEndpointBytes {
			return nil, fmt.Errorf("%w: invalid candidate endpoint", ErrInvalidRecord)
		}
		if _, duplicate := seenCandidates[endpoint]; duplicate {
			continue
		}
		seenCandidates[endpoint] = struct{}{}
		for _, tips := range s.endpointTips[tip.TenantScope][endpoint] {
			for _, residencyKey := range tips {
				entry, exists := s.residencies[residencyKey]
				if !exists {
					continue
				}
				if !entry.residency.ExpiresAt.After(now) {
					s.removeResidencyLocked(residencyKey)
					continue
				}
				s.removeNodeIfExpiredLocked(entry.residency.Node, now)
				if s.nodes[entry.residency.Node] == nil {
					continue
				}
				if entry.residency.Extent.Unit != requested.Unit {
					return nil, fmt.Errorf("%w: requested=%s residency=%s", ErrExtentUnit,
						requested.Unit, entry.residency.Extent.Unit)
				}
				matchedNode, matchedExtent, found, lcaErr := s.deepestCommonAncestorLocked(
					ctx, incomingAncestors, entry.residency.Node, now, &coverageSteps)
				if lcaErr != nil {
					return nil, lcaErr
				}
				if !found {
					continue
				}
				if entry.residency.Extent.Value < matchedExtent.Value {
					matchedExtent.Value = entry.residency.Extent.Value
				}
				matchedNodeCopy := matchedNode
				matchedExtent.Value = min(matchedExtent.Value, requested.Value)
				fraction := 0.0
				if requested.Value > 0 {
					fraction = float64(matchedExtent.Value) / float64(requested.Value)
				}
				choice := coverageChoice{
					coverage: EndpointCoverage{
						Endpoint:    endpoint,
						Tier:        entry.residency.Tier,
						MatchedNode: &matchedNodeCopy,
						Covered:     matchedExtent,
						Requested:   requested,
						Fraction:    fraction,
					},
					observedAt:    entry.residency.ObservedAt,
					residencyNode: entry.residency.Node,
				}
				resultKey := endpointTierKey{endpoint: endpoint, tier: entry.residency.Tier}
				if current, exists := byEndpointTier[resultKey]; !exists || choice.betterThan(current) {
					byEndpointTier[resultKey] = choice
				}
				s.residencyLRU.MoveToFront(entry.element)
			}
		}
	}

	result := make([]EndpointCoverage, 0, len(byEndpointTier))
	for _, choice := range byEndpointTier {
		result = append(result, choice.coverage)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Endpoint != result[j].Endpoint {
			return result[i].Endpoint < result[j].Endpoint
		}
		return result[i].Tier < result[j].Tier
	})
	return result, nil
}

type endpointTierKey struct {
	endpoint string
	tier     string
}

type coverageChoice struct {
	coverage      EndpointCoverage
	observedAt    time.Time
	residencyNode NodeKey
}

func (c coverageChoice) betterThan(other coverageChoice) bool {
	if c.coverage.Covered.Value != other.coverage.Covered.Value {
		return c.coverage.Covered.Value > other.coverage.Covered.Value
	}
	if !c.observedAt.Equal(other.observedAt) {
		return c.observedAt.After(other.observedAt)
	}
	return bytes.Compare(c.residencyNode.NodeID[:], other.residencyNode.NodeID[:]) < 0
}

// PurgeEndpoint removes every residency for one endpoint.
func (s *MemoryStore) PurgeEndpoint(ctx context.Context, endpoint string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if endpoint == "" || len(endpoint) > maxEndpointBytes {
		return fmt.Errorf("%w: endpoint is empty or too large", ErrInvalidRecord)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		processed := 0
		for key := range s.residenciesByEndpoint[endpoint] {
			s.removeResidencyLocked(key)
			processed++
			if processed == cleanupBatchSize {
				break
			}
		}
		done := len(s.residenciesByEndpoint[endpoint]) == 0
		s.mu.Unlock()
		if done {
			return nil
		}
		runtime.Gosched()
	}
}

func (s *MemoryStore) expire(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked(now)
}

// Stats returns bounded cardinalities without exposing keys.
func (s *MemoryStore) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Stats{
		Nodes:              len(s.nodes),
		Aliases:            len(s.aliases),
		Residencies:        len(s.residencies),
		ResidencyEndpoints: len(s.residenciesByEndpoint),
	}
}

func (s *MemoryStore) scheduleNodeExpiryLocked(key NodeKey, expiresAt time.Time) {
	entry := s.nodes[key]
	if entry.expiry != nil {
		entry.expiry.due = expiresAt
		heap.Fix(&s.expiries, entry.expiry.index)
		return
	}
	entry.expiry = &expiryItem{kind: nodeExpiry, due: expiresAt, node: key}
	heap.Push(&s.expiries, entry.expiry)
}

func (s *MemoryStore) scheduleAliasExpiryLocked(key AliasKey, expiresAt time.Time) {
	entry := s.aliases[key]
	if entry.expiry != nil {
		entry.expiry.due = expiresAt
		heap.Fix(&s.expiries, entry.expiry.index)
		return
	}
	entry.expiry = &expiryItem{kind: aliasExpiry, due: expiresAt, alias: key}
	heap.Push(&s.expiries, entry.expiry)
}

func (s *MemoryStore) scheduleResidencyExpiryLocked(key residencyKey, expiresAt time.Time) {
	entry := s.residencies[key]
	if entry.expiry != nil {
		entry.expiry.due = expiresAt
		heap.Fix(&s.expiries, entry.expiry.index)
		return
	}
	entry.expiry = &expiryItem{
		kind: residencyExpiry, due: expiresAt, residency: key,
	}
	heap.Push(&s.expiries, entry.expiry)
}

func (s *MemoryStore) removeExpiryLocked(item *expiryItem) {
	if item != nil && item.index >= 0 {
		heap.Remove(&s.expiries, item.index)
	}
}

func (s *MemoryStore) expireLocked(now time.Time) {
	for s.expireBatchLocked(now, 0) {
	}
}

func (s *MemoryStore) expireBatchLocked(now time.Time, limit int) bool {
	processed := 0
	for len(s.expiries) > 0 && !s.expiries[0].due.After(now) &&
		(limit <= 0 || processed < limit) {
		processed++
		item := heap.Pop(&s.expiries).(*expiryItem)
		switch item.kind {
		case aliasExpiry:
			entry := s.aliases[item.alias]
			if entry != nil && entry.expiry == item && !entry.expiresAt.After(now) {
				entry.expiry = nil
				s.removeAliasLocked(item.alias)
			}
		case residencyExpiry:
			entry := s.residencies[item.residency]
			if entry != nil && entry.expiry == item &&
				!entry.residency.ExpiresAt.After(now) {
				entry.expiry = nil
				s.removeResidencyLocked(item.residency)
			}
		case nodeExpiry:
			entry := s.nodes[item.node]
			if entry == nil || entry.expiry != item || entry.expiresAt.After(now) {
				continue
			}
			entry.expiry = nil
			if len(s.children[item.node]) != 0 {
				item.due = now.Add(s.config.CleanupInterval)
				entry.expiry = item
				heap.Push(&s.expiries, item)
				continue
			}
			s.removeExpiredNodeAndAncestorsLocked(item.node, now)
		}
	}
	return len(s.expiries) > 0 && !s.expiries[0].due.After(now)
}

func (s *MemoryStore) removeExpiredNodeAndAncestorsLocked(key NodeKey, now time.Time) {
	current := key
	for {
		entry := s.nodes[current]
		if entry == nil || entry.expiresAt.After(now) || len(s.children[current]) != 0 {
			return
		}
		if len(s.aliasesByNode[current]) != 0 || len(s.residenciesByNode[current]) != 0 {
			s.scheduleNodeExpiryLocked(current, now.Add(s.config.CleanupInterval))
			return
		}
		parent := cloneNodeKey(entry.node.Parent)
		if !s.removeNodeLocked(current) || parent == nil {
			return
		}
		current = *parent
	}
}

func (s *MemoryStore) compactEndpointTipsLocked(
	scope TenantScope,
	endpoint string,
	tier string,
	newKey residencyKey,
) error {
	tips := s.endpointTips[scope][endpoint][tier]
	if tips == nil {
		return nil
	}
	for nodeKey, existingKey := range tips {
		if existingKey == newKey {
			continue
		}
		existingAncestor, err := s.isAncestorLocked(nodeKey, newKey.Node)
		if err != nil {
			return err
		}
		if existingAncestor {
			existing := s.residencies[existingKey]
			added := s.residencies[newKey]
			if existing != nil && added != nil &&
				added.residency.Extent.Value >= existing.residency.Extent.Value {
				s.removeResidencyLocked(existingKey)
			}
			continue
		}
		newAncestor, err := s.isAncestorLocked(newKey.Node, nodeKey)
		if err != nil {
			return err
		}
		if newAncestor {
			existing := s.residencies[existingKey]
			added := s.residencies[newKey]
			if existing != nil && added != nil &&
				existing.residency.Extent.Value >= added.residency.Extent.Value {
				s.removeResidencyLocked(newKey)
				return nil
			}
		}
	}

	for s.endpointTipCountLocked(scope, endpoint) > s.config.MaxTipsPerEndpoint {
		oldest, ok := s.oldestEndpointTipLocked(scope, endpoint)
		if !ok {
			break
		}
		s.removeResidencyLocked(oldest)
	}
	return nil
}

func (s *MemoryStore) endpointTipCountLocked(scope TenantScope, endpoint string) int {
	count := 0
	for _, tips := range s.endpointTips[scope][endpoint] {
		count += len(tips)
	}
	return count
}

func (s *MemoryStore) oldestEndpointTipLocked(
	scope TenantScope,
	endpoint string,
) (residencyKey, bool) {
	var selected residencyKey
	var selectedAt time.Time
	found := false
	for _, tips := range s.endpointTips[scope][endpoint] {
		for _, key := range tips {
			entry, ok := s.residencies[key]
			if !ok {
				continue
			}
			if !found || entry.residency.ObservedAt.Before(selectedAt) ||
				(entry.residency.ObservedAt.Equal(selectedAt) &&
					residencyKeyLess(key, selected)) {
				selected = key
				selectedAt = entry.residency.ObservedAt
				found = true
			}
		}
	}
	return selected, found
}

func residencyKeyLess(left, right residencyKey) bool {
	if comparison := bytes.Compare(left.Node.NodeID[:], right.Node.NodeID[:]); comparison != 0 {
		return comparison < 0
	}
	if left.Tier != right.Tier {
		return left.Tier < right.Tier
	}
	return left.Endpoint < right.Endpoint
}

func (s *MemoryStore) ancestorSetLocked(
	ctx context.Context,
	start NodeKey,
	now time.Time,
	coverageSteps *int,
) (map[NodeKey]struct{}, error) {
	ancestors := make(map[NodeKey]struct{})
	current := start
	for depth := 0; ; depth++ {
		if err := s.consumeCoverageStep(ctx, coverageSteps); err != nil {
			return nil, err
		}
		if depth >= s.config.MaxAncestryDepth {
			return nil, ErrAncestryTooDeep
		}
		entry, ok := s.nodes[current]
		if !ok {
			break
		}
		ancestors[current] = struct{}{}
		s.touchNodeLocked(entry, now)
		if entry.node.Parent == nil {
			break
		}
		current = *entry.node.Parent
	}
	return ancestors, nil
}

func (s *MemoryStore) deepestCommonAncestorLocked(
	ctx context.Context,
	incoming map[NodeKey]struct{},
	other NodeKey,
	now time.Time,
	coverageSteps *int,
) (NodeKey, Extent, bool, error) {
	current := other
	for depth := 0; ; depth++ {
		if err := s.consumeCoverageStep(ctx, coverageSteps); err != nil {
			return NodeKey{}, Extent{}, false, err
		}
		if depth >= s.config.MaxAncestryDepth {
			return NodeKey{}, Extent{}, false, ErrAncestryTooDeep
		}
		entry, ok := s.nodes[current]
		if !ok {
			return NodeKey{}, Extent{}, false, nil
		}
		s.touchNodeLocked(entry, now)
		if _, common := incoming[current]; common {
			return current, entry.node.CumulativeExtent, true, nil
		}
		if entry.node.Parent == nil {
			return NodeKey{}, Extent{}, false, nil
		}
		current = *entry.node.Parent
	}
}

func (s *MemoryStore) consumeCoverageStep(ctx context.Context, steps *int) error {
	if *steps%256 == 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if *steps >= s.config.MaxCoverageSteps {
		return ErrCoverageBudget
	}
	*steps++
	return nil
}

func (s *MemoryStore) isAncestorLocked(ancestor, descendant NodeKey) (bool, error) {
	current := descendant
	for depth := 0; ; depth++ {
		if depth >= s.config.MaxAncestryDepth {
			return false, ErrAncestryTooDeep
		}
		if current == ancestor {
			return true, nil
		}
		entry, ok := s.nodes[current]
		if !ok || entry.node.Parent == nil {
			return false, nil
		}
		current = *entry.node.Parent
	}
}

func (s *MemoryStore) touchNodeLocked(entry *nodeEntry, now time.Time) {
	entry.expiresAt = now.Add(s.config.NodeTTL)
	s.nodeLRU.MoveToFront(entry.element)
	s.scheduleNodeExpiryLocked(entry.node.Key, entry.expiresAt)
}

func (s *MemoryStore) enforceNodeCapacity() {
	for {
		s.mu.Lock()
		processed := 0
		for len(s.nodes) > s.config.NodeCapacity && processed < cleanupBatchSize {
			if !s.evictOldestNodeStepLocked() {
				break
			}
			processed++
		}
		done := len(s.nodes) <= s.config.NodeCapacity || processed == 0
		s.mu.Unlock()
		if done {
			return
		}
		runtime.Gosched()
	}
}

func (s *MemoryStore) evictOldestNodeStepLocked() bool {
	for element := s.nodeLRU.Back(); element != nil; element = element.Prev() {
		key := element.Value.(NodeKey)
		if len(s.children[key]) != 0 {
			continue
		}
		for aliasKey := range s.aliasesByNode[key] {
			s.removeAliasLocked(aliasKey)
			return true
		}
		for residencyKey := range s.residenciesByNode[key] {
			s.removeResidencyLocked(residencyKey)
			return true
		}
		if s.removeNodeLocked(key) {
			return true
		}
	}
	return false
}

func (s *MemoryStore) evictOldestAliasLocked() {
	element := s.aliasLRU.Back()
	if element != nil {
		s.removeAliasLocked(element.Value.(AliasKey))
	}
}

func (s *MemoryStore) evictOldestResidencyLocked() {
	element := s.residencyLRU.Back()
	if element != nil {
		s.removeResidencyLocked(element.Value.(residencyKey))
	}
}

func (s *MemoryStore) removeNodeIfExpiredLocked(key NodeKey, now time.Time) {
	entry := s.nodes[key]
	if entry != nil && !entry.expiresAt.After(now) && len(s.children[key]) == 0 {
		s.removeNodeLocked(key)
	}
}

func (s *MemoryStore) removeNodeLocked(key NodeKey) bool {
	entry, ok := s.nodes[key]
	if !ok || len(s.children[key]) != 0 ||
		len(s.aliasesByNode[key]) != 0 || len(s.residenciesByNode[key]) != 0 {
		return false
	}
	delete(s.aliasesByNode, key)
	delete(s.residenciesByNode, key)
	if entry.node.Parent != nil {
		delete(s.children[*entry.node.Parent], key)
		if len(s.children[*entry.node.Parent]) == 0 {
			delete(s.children, *entry.node.Parent)
		}
	}
	delete(s.children, key)
	s.removeExpiryLocked(entry.expiry)
	s.nodeLRU.Remove(entry.element)
	delete(s.nodes, key)
	return true
}

func (s *MemoryStore) removeAliasLocked(key AliasKey) {
	entry, ok := s.aliases[key]
	if !ok {
		return
	}
	node := entry.target.Node
	s.unlinkAliasFromNodeLocked(key, node)
	s.removeExpiryLocked(entry.expiry)
	s.aliasLRU.Remove(entry.element)
	delete(s.aliases, key)
	s.removeExpiredNodeAndAncestorsLocked(node, s.clock.Now())
}

func (s *MemoryStore) removeResidencyLocked(key residencyKey) {
	entry, ok := s.residencies[key]
	if !ok {
		return
	}
	if tiers := s.endpointTips[key.Node.TenantScope][key.Endpoint]; tiers != nil {
		if tips := tiers[key.Tier]; tips != nil {
			delete(tips, key.Node)
			if len(tips) == 0 {
				delete(tiers, key.Tier)
			}
		}
		if len(tiers) == 0 {
			delete(s.endpointTips[key.Node.TenantScope], key.Endpoint)
			if len(s.endpointTips[key.Node.TenantScope]) == 0 {
				delete(s.endpointTips, key.Node.TenantScope)
			}
		}
	}
	if byNode := s.residenciesByNode[key.Node]; byNode != nil {
		delete(byNode, key)
		if len(byNode) == 0 {
			delete(s.residenciesByNode, key.Node)
		}
	}
	if byEndpoint := s.residenciesByEndpoint[key.Endpoint]; byEndpoint != nil {
		delete(byEndpoint, key)
		if len(byEndpoint) == 0 {
			delete(s.residenciesByEndpoint, key.Endpoint)
		}
	}
	s.removeExpiryLocked(entry.expiry)
	s.residencyLRU.Remove(entry.element)
	delete(s.residencies, key)
	s.removeExpiredNodeAndAncestorsLocked(key.Node, s.clock.Now())
}

func (s *MemoryStore) linkAliasToNodeLocked(key AliasKey, node NodeKey) {
	if s.aliasesByNode[node] == nil {
		s.aliasesByNode[node] = make(map[AliasKey]struct{})
	}
	s.aliasesByNode[node][key] = struct{}{}
}

func (s *MemoryStore) unlinkAliasFromNodeLocked(key AliasKey, node NodeKey) {
	if aliases := s.aliasesByNode[node]; aliases != nil {
		delete(aliases, key)
		if len(aliases) == 0 {
			delete(s.aliasesByNode, node)
		}
	}
}

func (s *MemoryStore) linkResidencyToNodeLocked(key residencyKey) {
	if s.residenciesByNode[key.Node] == nil {
		s.residenciesByNode[key.Node] = make(map[residencyKey]struct{})
	}
	s.residenciesByNode[key.Node][key] = struct{}{}
	if s.residenciesByEndpoint[key.Endpoint] == nil {
		s.residenciesByEndpoint[key.Endpoint] = make(map[residencyKey]struct{})
	}
	s.residenciesByEndpoint[key.Endpoint][key] = struct{}{}
}

func nodesEqual(left, right Node) bool {
	if left.Key != right.Key || left.CumulativeExtent != right.CumulativeExtent {
		return false
	}
	if left.Parent == nil || right.Parent == nil {
		return left.Parent == nil && right.Parent == nil
	}
	return *left.Parent == *right.Parent
}

func cloneNodeKey(key *NodeKey) *NodeKey {
	if key == nil {
		return nil
	}
	cloned := *key
	return &cloned
}

func cloneAliasTarget(target AliasTarget) AliasTarget {
	cloned := target
	if target.Session != nil {
		session := *target.Session
		cloned.Session = &session
	}
	return cloned
}

func validateNodeKey(key NodeKey) error {
	if key.TenantScope == "" || len(key.TenantScope) > maxTenantScopeBytes ||
		key.NodeID == (NodeID{}) {
		return ErrInvalidKey
	}
	return nil
}

func validateAliasKey(key AliasKey) error {
	if key.TenantScope == "" || len(key.TenantScope) > maxTenantScopeBytes ||
		key.Kind == "" || key.Value == "" || len(key.Value) > maxAliasValueBytes {
		return ErrInvalidAlias
	}
	switch key.Kind {
	case AliasKindResponse,
		AliasKindPromptCacheKey,
		AliasKindSession,
		AliasKindConversation,
		AliasKindFork,
		AliasKindSubAgent:
	default:
		return fmt.Errorf("%w: unsupported alias kind %q", ErrInvalidAlias, key.Kind)
	}
	return nil
}

func validateResidency(residency Residency) error {
	if err := validateNodeKey(residency.Node); err != nil {
		return fmt.Errorf("%w: invalid node", ErrInvalidRecord)
	}
	if residency.Endpoint == "" || len(residency.Endpoint) > maxEndpointBytes ||
		residency.Generation == 0 ||
		len(residency.Tier) > maxTierBytes {
		return ErrInvalidRecord
	}
	return validateExtentUnit(residency.Extent.Unit)
}

func validateExtentUnit(unit ExtentUnit) error {
	switch unit {
	case ExtentUnitFramedBytes, ExtentUnitModelTokens, ExtentUnitTokenBlocks:
		return nil
	default:
		return fmt.Errorf("%w: unsupported extent unit %q", ErrExtentUnit, unit)
	}
}
