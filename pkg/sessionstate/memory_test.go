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
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const testGeneration EndpointGeneration = 1

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

type cancelAfterChecksContext struct {
	context.Context
	checks      int
	cancelAfter int
}

func (c *cancelAfterChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAfter {
		return context.Canceled
	}
	return nil
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(duration time.Duration) {
	f.mu.Lock()
	f.now = f.now.Add(duration)
	f.mu.Unlock()
}

func testConfig() Config {
	config := DefaultConfig()
	config.NodeCapacity = 100
	config.AliasCapacity = 100
	config.ResidencyCapacity = 100
	config.MaxTipsPerEndpoint = 32
	config.CleanupInterval = 24 * time.Hour
	return config
}

func newTestStore(t *testing.T, config Config) (*MemoryStore, *fakeClock) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	clock := &fakeClock{now: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)}
	store, err := newMemoryStore(ctx, config, withClock(clock))
	require.NoError(t, err)
	return store, clock
}

func key(scope TenantScope, id string) NodeKey {
	return NodeKey{TenantScope: scope, NodeID: NodeID(sha256.Sum256([]byte(id)))}
}

func node(nodeKey NodeKey, parent *NodeKey, extent uint64) Node {
	return Node{
		Key:              nodeKey,
		Parent:           parent,
		CumulativeExtent: Extent{Value: extent, Unit: ExtentUnitFramedBytes},
	}
}

func putNode(t *testing.T, store Store, value Node) {
	t.Helper()
	require.NoError(t, store.PutNode(context.Background(), value))
}

func recordResidency(
	t *testing.T,
	store Store,
	nodeKey NodeKey,
	extent uint64,
) {
	t.Helper()
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: nodeKey, Endpoint: "pod-a", Generation: testGeneration,
		Extent: Extent{Value: extent, Unit: ExtentUnitFramedBytes},
	}))
}

func TestMemoryStoreSharedNodesAndTypedAliases(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("tenant=acme|model=qwen3|salt=public|frame=v1")
	c0 := key(scope, "c0")
	c1 := key(scope, "c1")
	putNode(t, store, node(c0, nil, 10))
	putNode(t, store, node(c1, &c0, 20))

	sessionA := SessionRef{TenantScope: scope, SessionID: "A"}
	sessionB := SessionRef{TenantScope: scope, SessionID: "B"}
	responseA := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-a"}
	responseB := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-b"}
	require.NoError(t, store.PutAlias(context.Background(), responseA,
		AliasTarget{Node: c1, Session: &sessionA}, time.Hour))
	require.NoError(t, store.PutAlias(context.Background(), responseB,
		AliasTarget{Node: c1, Session: &sessionB}, time.Hour))

	targetA, ok := store.ResolveAlias(context.Background(), responseA)
	require.True(t, ok)
	require.Equal(t, c1, targetA.Node)
	require.Equal(t, "A", targetA.Session.SessionID)
	targetB, ok := store.ResolveAlias(context.Background(), responseB)
	require.True(t, ok)
	require.Equal(t, c1, targetB.Node)
	require.Equal(t, "B", targetB.Session.SessionID)
	require.Equal(t, Stats{Nodes: 2, Aliases: 2}, store.Stats())
}

func TestMemoryStoreAliasRebindingUpdatesReverseIndexes(t *testing.T) {
	config := testConfig()
	config.NodeCapacity = 2
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	nodeA := key(scope, "a")
	nodeB := key(scope, "b")
	nodeC := key(scope, "c")
	putNode(t, store, node(nodeA, nil, 10))
	putNode(t, store, node(nodeB, nil, 10))
	alias := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp"}
	require.NoError(t, store.PutAlias(
		context.Background(), alias, AliasTarget{Node: nodeA}, time.Hour,
	))
	require.NoError(t, store.PutAlias(
		context.Background(), alias, AliasTarget{Node: nodeB}, time.Hour,
	))

	putNode(t, store, node(nodeC, nil, 10))
	target, found := store.ResolveAlias(context.Background(), alias)
	require.True(t, found)
	require.Equal(t, nodeB, target.Node)
	require.NotContains(t, store.aliasesByNode, nodeA)
	require.Contains(t, store.aliasesByNode[nodeB], alias)
}

func TestMemoryStoreAliasTenantScopeIsolation(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scopeA := TenantScope("tenant-a")
	scopeB := TenantScope("tenant-b")
	nodeA := key(scopeA, "same-content-id")
	nodeAResponse := key(scopeA, "response-content-id")
	nodeB := key(scopeB, "same-content-id")
	putNode(t, store, node(nodeA, nil, 10))
	putNode(t, store, node(nodeAResponse, nil, 12))
	putNode(t, store, node(nodeB, nil, 10))

	aliasA := AliasKey{TenantScope: scopeA, Kind: AliasKindSession, Value: "S17"}
	aliasB := AliasKey{TenantScope: scopeB, Kind: AliasKindSession, Value: "S17"}
	responseA := AliasKey{TenantScope: scopeA, Kind: AliasKindResponse, Value: "S17"}
	require.NoError(t, store.PutAlias(context.Background(), aliasA, AliasTarget{Node: nodeA}, time.Hour))
	require.NoError(t, store.PutAlias(context.Background(), aliasB, AliasTarget{Node: nodeB}, time.Hour))
	require.NoError(t, store.PutAlias(
		context.Background(), responseA, AliasTarget{Node: nodeAResponse}, time.Hour,
	))

	targetA, ok := store.ResolveAlias(context.Background(), aliasA)
	require.True(t, ok)
	require.Equal(t, nodeA, targetA.Node)
	targetB, ok := store.ResolveAlias(context.Background(), aliasB)
	require.True(t, ok)
	require.Equal(t, nodeB, targetB.Node)
	responseTarget, ok := store.ResolveAlias(context.Background(), responseA)
	require.True(t, ok)
	require.Equal(t, nodeAResponse, responseTarget.Node)
}

func TestMemoryStoreCoverageAndTipBoundsAreTenantIsolated(t *testing.T) {
	config := testConfig()
	config.MaxTipsPerEndpoint = 1
	store, clock := newTestStore(t, config)
	scopeA := TenantScope("tenant-a")
	scopeB := TenantScope("tenant-b")
	rootA := key(scopeA, "root")
	oldA := key(scopeA, "old")
	tipA := key(scopeA, "tip")
	rootB := key(scopeB, "root")
	oldB := key(scopeB, "old")
	tipB := key(scopeB, "tip")
	putNode(t, store, node(rootA, nil, 10))
	putNode(t, store, node(oldA, &rootA, 20))
	putNode(t, store, node(tipA, &rootA, 30))
	putNode(t, store, node(rootB, nil, 11))
	putNode(t, store, node(oldB, &rootB, 21))
	putNode(t, store, node(tipB, &rootB, 31))

	for _, residency := range []Residency{
		{Node: oldA, Endpoint: "shared-pod", Generation: testGeneration, Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes}},
		{Node: oldB, Endpoint: "shared-pod", Generation: testGeneration, Extent: Extent{Value: 21, Unit: ExtentUnitFramedBytes}},
	} {
		require.NoError(t, store.RecordEstimate(context.Background(), residency))
	}
	clock.Advance(time.Second)
	for _, residency := range []Residency{
		{Node: tipA, Endpoint: "shared-pod", Generation: testGeneration, Extent: Extent{Value: 30, Unit: ExtentUnitFramedBytes}},
		{Node: tipB, Endpoint: "shared-pod", Generation: testGeneration, Extent: Extent{Value: 31, Unit: ExtentUnitFramedBytes}},
	} {
		require.NoError(t, store.RecordEstimate(context.Background(), residency))
	}

	coverageA, err := store.Coverage(context.Background(), tipA,
		Extent{Value: 30, Unit: ExtentUnitFramedBytes}, []string{"shared-pod"})
	require.NoError(t, err)
	require.Len(t, coverageA, 1)
	require.Equal(t, tipA, *coverageA[0].MatchedNode)
	coverageB, err := store.Coverage(context.Background(), tipB,
		Extent{Value: 31, Unit: ExtentUnitFramedBytes}, []string{"shared-pod"})
	require.NoError(t, err)
	require.Len(t, coverageB, 1)
	require.Equal(t, tipB, *coverageB[0].MatchedNode)
	require.Equal(t, Stats{Nodes: 6, Residencies: 2, ResidencyEndpoints: 1}, store.Stats())
}

func TestMemoryStoreForkAwareCoverage(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	c1 := key(scope, "c1")
	c2a := key(scope, "c2-a")
	c2b := key(scope, "c2-b")
	c3a := key(scope, "c3-a")
	putNode(t, store, node(c0, nil, 10))
	putNode(t, store, node(c1, &c0, 20))
	putNode(t, store, node(c2a, &c1, 30))
	putNode(t, store, node(c2b, &c1, 35))
	putNode(t, store, node(c3a, &c2a, 40))

	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c2a, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 30, Unit: ExtentUnitFramedBytes},
	}))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c2b, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 35, Unit: ExtentUnitFramedBytes},
	}))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c1, Endpoint: "pod-b", Generation: testGeneration, Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))

	coverage, err := store.Coverage(context.Background(), c3a,
		Extent{Value: 40, Unit: ExtentUnitFramedBytes}, []string{"pod-a", "pod-b"})
	require.NoError(t, err)
	require.Len(t, coverage, 2)
	require.Equal(t, "pod-a", coverage[0].Endpoint)
	require.Equal(t, c2a, *coverage[0].MatchedNode)
	require.Equal(t, uint64(30), coverage[0].Covered.Value)
	require.InDelta(t, 0.75, coverage[0].Fraction, 0.0001)
	require.Equal(t, "pod-b", coverage[1].Endpoint)
	require.Equal(t, c1, *coverage[1].MatchedNode)
	require.InDelta(t, 0.5, coverage[1].Fraction, 0.0001)
}

func TestMemoryStoreOneNodeCanCoverManyEndpoints(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))
	for _, endpoint := range []string{"pod-a", "pod-b", "pod-c"} {
		require.NoError(t, store.RecordEstimate(context.Background(), Residency{
			Node: c0, Endpoint: endpoint, Generation: testGeneration,
			Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
		}))
	}

	coverage, err := store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes}, []string{"pod-a", "pod-b", "pod-c"})
	require.NoError(t, err)
	require.Len(t, coverage, 3)
	for index, endpoint := range []string{"pod-a", "pod-b", "pod-c"} {
		require.Equal(t, endpoint, coverage[index].Endpoint)
		require.Equal(t, c0, *coverage[index].MatchedNode)
		require.Equal(t, Extent{Value: 10, Unit: ExtentUnitFramedBytes}, coverage[index].Covered)
		require.Equal(t, Extent{Value: 10, Unit: ExtentUnitFramedBytes}, coverage[index].Requested)
		require.Equal(t, 1.0, coverage[index].Fraction)
	}
	require.Equal(t, Stats{Nodes: 1, Residencies: 3, ResidencyEndpoints: 3}, store.Stats())
}

func TestMemoryStoreCompactsDominatedAndBoundsForkTips(t *testing.T) {
	config := testConfig()
	config.MaxTipsPerEndpoint = 2
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	child := key(scope, "child")
	forkA := key(scope, "fork-a")
	forkB := key(scope, "fork-b")
	forkC := key(scope, "fork-c")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(child, &root, 20))
	putNode(t, store, node(forkA, &child, 30))
	putNode(t, store, node(forkB, &child, 31))
	putNode(t, store, node(forkC, &child, 32))

	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: root, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))
	clock.Advance(time.Second)
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: child, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))
	require.Equal(t, 1, store.Stats().Residencies, "child should dominate its root")
	childCoverage, err := store.Coverage(context.Background(), child,
		Extent{Value: 20, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, child, *childCoverage[0].MatchedNode)

	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: root, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))
	require.Equal(t, 1, store.Stats().Residencies, "root must not replace its descendant")
	childCoverage, err = store.Coverage(context.Background(), child,
		Extent{Value: 20, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, child, *childCoverage[0].MatchedNode)

	for index, fork := range []NodeKey{forkA, forkB, forkC} {
		clock.Advance(time.Second)
		require.NoError(t, store.RecordEstimate(context.Background(), Residency{
			Node: fork, Endpoint: "pod-a", Generation: testGeneration,
			Extent: Extent{Value: uint64(30 + index), Unit: ExtentUnitFramedBytes},
		}))
	}
	require.Equal(t, 2, store.Stats().Residencies)
	forkACoverage, err := store.Coverage(context.Background(), forkA,
		Extent{Value: 30, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, child, *forkACoverage[0].MatchedNode, "oldest fork-a tip should be evicted")
	for _, survivingFork := range []struct {
		key    NodeKey
		extent uint64
	}{
		{key: forkB, extent: 31},
		{key: forkC, extent: 32},
	} {
		coverage, coverageErr := store.Coverage(context.Background(), survivingFork.key,
			Extent{Value: survivingFork.extent, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
		require.NoError(t, coverageErr)
		require.Equal(t, survivingFork.key, *coverage[0].MatchedNode)
	}
}

func TestMemoryStorePartialDescendantDoesNotDiscardStrongerAncestor(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	root := key(scope, "root")
	child := key(scope, "child")
	putNode(t, store, node(root, nil, 100))
	putNode(t, store, node(child, &root, 120))
	recordResidency(t, store, root, 100)
	recordResidency(t, store, child, 20)
	require.Equal(t, 2, store.Stats().Residencies)

	coverage, err := store.Coverage(context.Background(), child,
		Extent{Value: 120, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	require.Equal(t, uint64(100), coverage[0].Covered.Value)
	require.Equal(t, root, *coverage[0].MatchedNode)

	recordResidency(t, store, child, 120)
	require.Equal(t, 1, store.Stats().Residencies)
	coverage, err = store.Coverage(context.Background(), child,
		Extent{Value: 120, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, uint64(120), coverage[0].Covered.Value)
	require.Equal(t, child, *coverage[0].MatchedNode)

	reverseStore, _ := newTestStore(t, testConfig())
	putNode(t, reverseStore, node(root, nil, 100))
	putNode(t, reverseStore, node(child, &root, 120))
	recordResidency(t, reverseStore, child, 20)
	recordResidency(t, reverseStore, root, 100)
	require.Equal(t, 2, reverseStore.Stats().Residencies)
	coverage, err = reverseStore.Coverage(context.Background(), child,
		Extent{Value: 120, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, uint64(100), coverage[0].Covered.Value)
	require.Equal(t, root, *coverage[0].MatchedNode)
}

func TestMemoryStoreTipBoundUsesDeterministicNodeIDTieBreak(t *testing.T) {
	config := testConfig()
	config.MaxTipsPerEndpoint = 1
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	forkA := NodeKey{TenantScope: scope, NodeID: NodeID{1}}
	forkB := NodeKey{TenantScope: scope, NodeID: NodeID{2}}
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(forkA, &root, 20))
	putNode(t, store, node(forkB, &root, 21))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: forkB, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 21, Unit: ExtentUnitFramedBytes},
	}))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: forkA, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))

	coverageA, err := store.Coverage(context.Background(), forkA,
		Extent{Value: 20, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, root, *coverageA[0].MatchedNode, "lexicographically smaller tied tip should be evicted")
	coverageB, err := store.Coverage(context.Background(), forkB,
		Extent{Value: 21, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, forkB, *coverageB[0].MatchedNode)
}

func TestMemoryStoreReturnsDeterministicCoveragePerTier(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	root := key(scope, "root")
	forkA := key(scope, "fork-a")
	forkB := key(scope, "fork-b")
	incoming := key(scope, "incoming")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(forkA, &root, 20))
	putNode(t, store, node(forkB, &root, 20))
	putNode(t, store, node(incoming, &root, 30))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: forkA, Endpoint: "pod-a", Generation: testGeneration, Tier: "GPU",
		Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: forkB, Endpoint: "pod-a", Generation: testGeneration, Tier: "CPU",
		Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))
	require.Equal(t, 2, store.Stats().Residencies)

	coverage, err := store.Coverage(context.Background(), incoming,
		Extent{Value: 30, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Len(t, coverage, 2)
	require.Equal(t, root, *coverage[0].MatchedNode)
	require.Equal(t, "CPU", coverage[0].Tier)
	require.Equal(t, uint64(10), coverage[0].Covered.Value)
	require.InDelta(t, 1.0/3.0, coverage[0].Fraction, 0.0001)
	require.Equal(t, root, *coverage[1].MatchedNode)
	require.Equal(t, "GPU", coverage[1].Tier)
	require.Equal(t, uint64(10), coverage[1].Covered.Value)
	require.InDelta(t, 1.0/3.0, coverage[1].Fraction, 0.0001)
	require.NoError(t, store.PurgeEndpoint(context.Background(), "pod-a"))
	require.Zero(t, store.Stats().Residencies)
}

func TestMemoryStoreTipBoundAppliesAcrossTiers(t *testing.T) {
	config := testConfig()
	config.MaxTipsPerEndpoint = 1
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	cpuTip := key(scope, "cpu")
	gpuTip := key(scope, "gpu")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(cpuTip, &root, 20))
	putNode(t, store, node(gpuTip, &root, 20))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: cpuTip, Endpoint: "pod-a", Generation: testGeneration, Tier: "CPU",
		Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))
	clock.Advance(time.Second)
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: gpuTip, Endpoint: "pod-a", Generation: testGeneration, Tier: "GPU",
		Extent: Extent{Value: 20, Unit: ExtentUnitFramedBytes},
	}))

	require.Equal(t, 1, store.Stats().Residencies)
	coverage, err := store.Coverage(context.Background(), gpuTip,
		Extent{Value: 20, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	require.Equal(t, "GPU", coverage[0].Tier)
	require.Equal(t, gpuTip, *coverage[0].MatchedNode)
}

func TestMemoryStoreClampsCoverageToRequestedExtent(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	root := key(scope, "root")
	putNode(t, store, node(root, nil, 10))
	recordResidency(t, store, root, 10)

	coverage, err := store.Coverage(context.Background(), root,
		Extent{Value: 5, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Equal(t, uint64(5), coverage[0].Covered.Value)
	require.Equal(t, 1.0, coverage[0].Fraction)

	coverage, err = store.Coverage(context.Background(), root,
		Extent{Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Zero(t, coverage[0].Covered.Value)
	require.Zero(t, coverage[0].Fraction)
}

func TestMemoryStorePinsAncestorsUntilChildrenExpire(t *testing.T) {
	config := testConfig()
	config.NodeTTL = time.Minute
	config.AliasTTL = time.Minute
	config.EstimateTTL = time.Minute
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	child := key(scope, "child")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(child, &root, 20))
	clock.Advance(30 * time.Second)
	alias := AliasKey{TenantScope: scope, Kind: AliasKindSession, Value: "S17"}
	require.NoError(t, store.PutAlias(
		context.Background(), alias, AliasTarget{Node: child}, time.Hour,
	))
	_, found := store.ResolveAlias(context.Background(), alias)
	require.True(t, found)

	clock.Advance(40 * time.Second)
	store.expire(clock.Now())
	require.Equal(t, 2, store.Stats().Nodes, "expired parent must remain pinned by its child")
	require.NoError(t, store.PutNode(context.Background(), node(child, &root, 20)))

	clock.Advance(2 * time.Minute)
	store.expire(clock.Now())
	require.Zero(t, store.Stats().Nodes, "expired leaf removal should release expired ancestors")
}

func TestMemoryStoreNodeLRUNeverLeavesDanglingParent(t *testing.T) {
	config := testConfig()
	config.NodeCapacity = 2
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	child := key(scope, "child")
	unrelated := key(scope, "unrelated")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(child, &root, 20))
	putNode(t, store, node(unrelated, nil, 5))

	require.Equal(t, 2, store.Stats().Nodes)
	_, err := store.Coverage(context.Background(), child,
		Extent{Value: 20, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.ErrorIs(t, err, ErrNodeNotFound)
	require.NoError(t, store.PutNode(context.Background(), node(child, &root, 20)),
		"evicted child must be insertable because its parent remains")
}

func TestMemoryStoreExpiryAndEndpointPurge(t *testing.T) {
	config := testConfig()
	config.NodeTTL = time.Hour
	config.AliasTTL = time.Hour
	config.EstimateTTL = 2 * time.Minute
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))
	alias := AliasKey{TenantScope: scope, Kind: AliasKindSession, Value: "S17"}
	require.NoError(t, store.PutAlias(context.Background(), alias, AliasTarget{Node: c0}, time.Hour))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-b", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))

	require.NoError(t, store.PurgeEndpoint(context.Background(), "pod-a"))
	require.Equal(t, 1, store.Stats().Residencies)
	coverage, err := store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes}, []string{"pod-a", "pod-b"})
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	require.Equal(t, "pod-b", coverage[0].Endpoint)

	clock.Advance(3 * time.Minute)
	store.expire(clock.Now())
	require.Equal(t, Stats{Nodes: 1, Aliases: 1}, store.Stats())

	clock.Advance(2 * time.Hour)
	store.expire(clock.Now())
	require.Equal(t, Stats{}, store.Stats())
}

func TestMemoryStorePurgeEndpointObservesCancellationBetweenBatches(t *testing.T) {
	config := testConfig()
	config.ResidencyCapacity = cleanupBatchSize + 1
	config.MaxTipsPerEndpoint = cleanupBatchSize + 1
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	putNode(t, store, node(root, nil, 10))
	for index := 0; index <= cleanupBatchSize; index++ {
		require.NoError(t, store.RecordEstimate(context.Background(), Residency{
			Node: root, Endpoint: "pod-a", Generation: testGeneration,
			Tier:   fmt.Sprintf("tier-%d", index),
			Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
		}))
	}
	ctx := &cancelAfterChecksContext{Context: context.Background(), cancelAfter: 3}

	err := store.PurgeEndpoint(ctx, "pod-a")
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, store.Stats().Residencies)
	require.NoError(t, store.PurgeEndpoint(context.Background(), "pod-a"))
	require.Zero(t, store.Stats().Residencies)
}

func TestMemoryStoreCooperativeExpiryObservesCancellationBetweenBatches(t *testing.T) {
	config := testConfig()
	config.NodeCapacity = 1
	config.AliasCapacity = expiryBatchSize + 1
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	putNode(t, store, node(root, nil, 10))
	for index := 0; index <= expiryBatchSize; index++ {
		require.NoError(t, store.PutAlias(context.Background(), AliasKey{
			TenantScope: scope,
			Kind:        AliasKindResponse,
			Value:       fmt.Sprintf("response-%d", index),
		}, AliasTarget{Node: root}, time.Hour))
	}
	clock.Advance(2 * time.Hour)
	ctx := &cancelAfterChecksContext{Context: context.Background(), cancelAfter: 2}

	store.expireCooperatively(ctx, clock.Now())
	require.NotZero(t, store.Stats().Aliases)
	store.expire(clock.Now())
	require.Equal(t, Stats{}, store.Stats())
}

func TestMemoryStoreExpiryIndexRemainsBoundedUnderRefresh(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	root := key(scope, "root")
	putNode(t, store, node(root, nil, 10))
	alias := AliasKey{TenantScope: scope, Kind: AliasKindSession, Value: "S17"}
	require.NoError(t, store.PutAlias(
		context.Background(), alias, AliasTarget{Node: root}, time.Hour,
	))
	for range 3 {
		_, found := store.ResolveAlias(context.Background(), alias)
		require.True(t, found)
		recordResidency(t, store, root, 10)
	}

	stats := store.Stats()
	require.Len(t, store.expiries, stats.Nodes+stats.Aliases+stats.Residencies)
	require.NoError(t, store.PurgeEndpoint(context.Background(), "pod-a"))
	stats = store.Stats()
	require.Len(t, store.expiries, stats.Nodes+stats.Aliases+stats.Residencies)
}

func TestMemoryStoreClampsCallerControlledLifetimes(t *testing.T) {
	config := testConfig()
	config.NodeTTL = 24 * time.Hour
	config.AliasTTL = time.Hour
	config.EstimateTTL = 2 * time.Minute
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))
	alias := AliasKey{TenantScope: scope, Kind: AliasKindSession, Value: "S17"}
	require.NoError(t, store.PutAlias(
		context.Background(), alias, AliasTarget{Node: c0}, 24*time.Hour,
	))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node:       c0,
		Endpoint:   "pod-a",
		Generation: testGeneration,
		Extent:     Extent{Value: 10, Unit: ExtentUnitFramedBytes},
		ObservedAt: clock.Now().Add(24 * time.Hour),
		ExpiresAt:  clock.Now().Add(48 * time.Hour),
	}))

	clock.Advance(3 * time.Minute)
	store.expire(clock.Now())
	require.Zero(t, store.Stats().Residencies, "estimate must use configured TTL")
	clock.Advance(time.Hour)
	store.expire(clock.Now())
	_, found := store.ResolveAlias(context.Background(), alias)
	require.False(t, found, "alias TTL override must not exceed configured maximum")
}

func TestMemoryStoreRejectsUnitMismatch(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))

	err := store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 1, Unit: ExtentUnitTokenBlocks},
	})
	require.ErrorIs(t, err, ErrExtentUnit)

	_, err = store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitModelTokens}, []string{"pod-a"})
	require.ErrorIs(t, err, ErrExtentUnit)
}

func TestMemoryStoreRejectsInvalidAndOversizedKeys(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	validNode := key(scope, "valid")
	putNode(t, store, node(validNode, nil, 10))

	err := store.PutNode(context.Background(), Node{
		Key: NodeKey{TenantScope: scope},
		CumulativeExtent: Extent{
			Value: 10,
			Unit:  ExtentUnitFramedBytes,
		},
	})
	require.ErrorIs(t, err, ErrInvalidKey)

	err = store.PutNode(context.Background(), Node{
		Key: NodeKey{
			TenantScope: TenantScope(strings.Repeat("t", maxTenantScopeBytes+1)),
			NodeID:      NodeID{1},
		},
		CumulativeExtent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, ErrInvalidKey)

	err = store.PutAlias(context.Background(), AliasKey{
		TenantScope: scope,
		Kind:        AliasKind("unknown"),
		Value:       "value",
	}, AliasTarget{Node: validNode}, time.Hour)
	require.ErrorIs(t, err, ErrInvalidAlias)

	err = store.PutAlias(context.Background(), AliasKey{
		TenantScope: scope,
		Kind:        AliasKindSession,
		Value:       strings.Repeat("a", maxAliasValueBytes+1),
	}, AliasTarget{Node: validNode}, time.Hour)
	require.ErrorIs(t, err, ErrInvalidAlias)

	err = store.RecordEstimate(context.Background(), Residency{
		Node:       validNode,
		Endpoint:   strings.Repeat("e", maxEndpointBytes+1),
		Generation: testGeneration,
		Extent:     Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, ErrInvalidRecord)

	unknownUnit := ExtentUnit(strings.Repeat("u", 4096))
	err = store.RecordEstimate(context.Background(), Residency{
		Node: validNode, Endpoint: "pod-a",
		Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, ErrInvalidRecord)
	err = store.PutNode(context.Background(), Node{
		Key:              key(scope, "unknown-unit"),
		CumulativeExtent: Extent{Value: 10, Unit: unknownUnit},
	})
	require.ErrorIs(t, err, ErrExtentUnit)
	err = store.RecordEstimate(context.Background(), Residency{
		Node: validNode, Endpoint: "pod-a", Generation: testGeneration,
		Extent: Extent{Value: 10, Unit: unknownUnit},
	})
	require.ErrorIs(t, err, ErrExtentUnit)
	_, err = store.Coverage(context.Background(), validNode,
		Extent{Value: 10, Unit: unknownUnit}, []string{"pod-a"})
	require.ErrorIs(t, err, ErrExtentUnit)
}

func TestMemoryStoreCapacitiesAreIndependent(t *testing.T) {
	config := testConfig()
	config.NodeCapacity = 2
	config.AliasCapacity = 2
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	c1 := key(scope, "c1")
	putNode(t, store, node(c0, nil, 10))
	putNode(t, store, node(c1, &c0, 20))

	for index := range 2 {
		require.NoError(t, store.PutAlias(context.Background(), AliasKey{
			TenantScope: scope,
			Kind:        AliasKindResponse,
			Value:       fmt.Sprintf("resp-%d", index),
		}, AliasTarget{Node: c1}, time.Hour))
	}
	alias0 := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-0"}
	_, found := store.ResolveAlias(context.Background(), alias0)
	require.True(t, found)
	alias2 := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-2"}
	require.NoError(t, store.PutAlias(
		context.Background(), alias2, AliasTarget{Node: c1}, time.Hour,
	))

	require.Equal(t, Stats{Nodes: 2, Aliases: 2}, store.Stats())
	_, found = store.ResolveAlias(context.Background(), AliasKey{
		TenantScope: scope, Kind: AliasKindResponse, Value: "resp-0",
	})
	require.True(t, found, "recently resolved alias should remain")
	_, found = store.ResolveAlias(context.Background(), AliasKey{
		TenantScope: scope, Kind: AliasKindResponse, Value: "resp-1",
	})
	require.False(t, found, "least recently used alias should be evicted")
	_, found = store.ResolveAlias(context.Background(), alias2)
	require.True(t, found)
}

func TestMemoryStoreResidencyCapacityUsesLRU(t *testing.T) {
	config := testConfig()
	config.ResidencyCapacity = 2
	store, clock := newTestStore(t, config)
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-a", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))
	clock.Advance(time.Second)
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-b", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))
	coverage, err := store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.NoError(t, err)
	require.Len(t, coverage, 1)
	clock.Advance(time.Second)
	require.NoError(t, store.RecordEstimate(context.Background(), Residency{
		Node: c0, Endpoint: "pod-c", Generation: testGeneration, Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
	}))

	coverage, err = store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes}, []string{"pod-a", "pod-b", "pod-c"})
	require.NoError(t, err)
	require.Len(t, coverage, 2)
	require.Equal(t, "pod-a", coverage[0].Endpoint)
	require.Equal(t, "pod-c", coverage[1].Endpoint)
}

func TestMemoryStoreNodeCapacityUsesAccessOrderedLRU(t *testing.T) {
	config := testConfig()
	config.NodeCapacity = 2
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	c1 := key(scope, "c1")
	c2 := key(scope, "c2")
	putNode(t, store, node(c0, nil, 10))
	putNode(t, store, node(c1, nil, 10))
	alias0 := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-0"}
	alias1 := AliasKey{TenantScope: scope, Kind: AliasKindResponse, Value: "resp-1"}
	require.NoError(t, store.PutAlias(context.Background(), alias0, AliasTarget{Node: c0}, time.Hour))
	require.NoError(t, store.PutAlias(context.Background(), alias1, AliasTarget{Node: c1}, time.Hour))

	_, ok := store.ResolveAlias(context.Background(), alias0)
	require.True(t, ok, "resolving c0 should make it most recently used")
	putNode(t, store, node(c2, nil, 10))

	_, ok = store.ResolveAlias(context.Background(), alias0)
	require.True(t, ok)
	_, ok = store.ResolveAlias(context.Background(), alias1)
	require.False(t, ok, "c1 and its dependent alias should be evicted")
	require.Equal(t, Stats{Nodes: 2, Aliases: 1}, store.Stats())
}

func TestMemoryStoreConcurrentAccess(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	putNode(t, store, node(c0, nil, 10))

	start := make(chan struct{})
	errs := make(chan error, 50)
	var waitGroup sync.WaitGroup
	for index := range 50 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			alias := AliasKey{
				TenantScope: scope,
				Kind:        AliasKindResponse,
				Value:       fmt.Sprintf("resp-%d", index),
			}
			if err := store.PutAlias(context.Background(), alias, AliasTarget{Node: c0}, time.Hour); err != nil {
				errs <- err
				return
			}
			_, ok := store.ResolveAlias(context.Background(), alias)
			if !ok {
				errs <- fmt.Errorf("alias %q was not resolved", alias.Value)
				return
			}
			if err := store.RecordEstimate(context.Background(), Residency{
				Node: c0, Endpoint: fmt.Sprintf("pod-%d", index%5), Generation: testGeneration,
				Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
			}); err != nil {
				errs <- err
				return
			}
			_, err := store.Coverage(context.Background(), c0,
				Extent{Value: 10, Unit: ExtentUnitFramedBytes},
				[]string{"pod-0", "pod-1", "pod-2", "pod-3", "pod-4"})
			if err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	require.Equal(t, 50, store.Stats().Aliases)
	require.Equal(t, 5, store.Stats().Residencies)
	coverage, err := store.Coverage(context.Background(), c0,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes},
		[]string{"pod-0", "pod-1", "pod-2", "pod-3", "pod-4"})
	require.NoError(t, err)
	require.Len(t, coverage, 5)
}

func TestMemoryStoreRejectsConflictingImmutableNode(t *testing.T) {
	store, _ := newTestStore(t, testConfig())
	scope := TenantScope("scope")
	c0 := key(scope, "c0")
	c1 := key(scope, "c1")
	c2 := key(scope, "c2")
	putNode(t, store, node(c0, nil, 10))
	putNode(t, store, node(c2, nil, 12))

	err := store.PutNode(context.Background(), Node{
		Key:              c0,
		CumulativeExtent: Extent{Value: 11, Unit: ExtentUnitFramedBytes},
	})
	require.ErrorIs(t, err, ErrNodeConflict)

	err = store.PutNode(context.Background(), node(key(scope, "missing-child"), &c1, 20))
	require.ErrorIs(t, err, ErrNodeNotFound)

	err = store.PutNode(context.Background(), Node{
		Key:    key(scope, "wrong-unit"),
		Parent: &c0,
		CumulativeExtent: Extent{
			Value: 20,
			Unit:  ExtentUnitModelTokens,
		},
	})
	require.ErrorIs(t, err, ErrExtentUnit)

	err = store.PutNode(context.Background(), node(key(scope, "decreasing"), &c0, 9))
	require.ErrorIs(t, err, ErrInvalidNode)

	otherScopeChild := key(TenantScope("other-scope"), "child")
	err = store.PutNode(context.Background(), node(otherScopeChild, &c0, 20))
	require.ErrorIs(t, err, ErrInvalidNode)

	err = store.PutNode(context.Background(), node(c0, &c0, 10))
	require.ErrorIs(t, err, ErrNodeConflict)
}

func TestMemoryStoreBoundsAncestryDepth(t *testing.T) {
	config := testConfig()
	config.MaxAncestryDepth = 2
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	child := key(scope, "child")
	grandchild := key(scope, "grandchild")
	putNode(t, store, node(root, nil, 10))
	putNode(t, store, node(child, &root, 20))

	err := store.PutNode(context.Background(), node(grandchild, &child, 30))
	require.ErrorIs(t, err, ErrAncestryTooDeep)
}

func TestMemoryStoreBoundsTotalCoverageWork(t *testing.T) {
	config := testConfig()
	config.MaxCoverageSteps = 1
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	root := key(scope, "root")
	putNode(t, store, node(root, nil, 10))
	recordResidency(t, store, root, 10)

	_, err := store.Coverage(context.Background(), root,
		Extent{Value: 10, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.ErrorIs(t, err, ErrCoverageBudget)
}

func TestMemoryStoreCoverageWorkObservesCancellation(t *testing.T) {
	config := testConfig()
	config.MaxAncestryDepth = 512
	config.NodeCapacity = 400
	store, _ := newTestStore(t, config)
	scope := TenantScope("scope")
	parent := key(scope, "node-0")
	putNode(t, store, node(parent, nil, 1))
	for index := 1; index < 300; index++ {
		child := key(scope, fmt.Sprintf("node-%d", index))
		putNode(t, store, node(child, &parent, uint64(index+1)))
		parent = child
	}
	recordResidency(t, store, parent, 300)
	ctx := &cancelAfterChecksContext{Context: context.Background(), cancelAfter: 3}

	_, err := store.Coverage(ctx, parent,
		Extent{Value: 300, Unit: ExtentUnitFramedBytes}, []string{"pod-a"})
	require.ErrorIs(t, err, context.Canceled)
}

func TestConfigRejectsDependentTTLsLongerThanNodeTTL(t *testing.T) {
	config := testConfig()
	config.NodeTTL = time.Minute
	config.AliasTTL = 2 * time.Minute
	require.ErrorIs(t, config.Validate(), ErrInvalidConfig)

	config = testConfig()
	config.NodeTTL = time.Minute
	config.EstimateTTL = 2 * time.Minute
	require.ErrorIs(t, config.Validate(), ErrInvalidConfig)
}

func BenchmarkMemoryStoreWorstCaseNodeRemoval(b *testing.B) {
	const entries = maxTipsPerEndpointLimit
	for range b.N {
		b.StopTimer()
		config := testConfig()
		config.NodeCapacity = 1
		config.AliasCapacity = entries
		config.ResidencyCapacity = entries
		config.MaxTipsPerEndpoint = entries
		ctx, cancel := context.WithCancel(context.Background())
		store, err := newMemoryStore(ctx, config, withClock(&fakeClock{now: time.Now()}))
		if err != nil {
			b.Fatal(err)
		}
		scope := TenantScope("scope")
		root := key(scope, "root")
		if err := store.PutNode(ctx, node(root, nil, 10)); err != nil {
			b.Fatal(err)
		}
		for index := range entries {
			if err := store.PutAlias(ctx, AliasKey{
				TenantScope: scope,
				Kind:        AliasKindResponse,
				Value:       fmt.Sprintf("response-%d", index),
			}, AliasTarget{Node: root}, time.Hour); err != nil {
				b.Fatal(err)
			}
			if err := store.RecordEstimate(ctx, Residency{
				Node: root, Endpoint: "pod-a", Generation: testGeneration,
				Tier:   fmt.Sprintf("tier-%d", index),
				Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
			}); err != nil {
				b.Fatal(err)
			}
		}
		replacement := key(scope, "replacement")
		b.StartTimer()
		err = store.PutNode(ctx, node(replacement, nil, 10))
		b.StopTimer()
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMemoryStoreWorstCaseEndpointPurge(b *testing.B) {
	const entries = maxTipsPerEndpointLimit
	for range b.N {
		b.StopTimer()
		config := testConfig()
		config.ResidencyCapacity = entries
		config.MaxTipsPerEndpoint = entries
		ctx, cancel := context.WithCancel(context.Background())
		store, err := newMemoryStore(ctx, config, withClock(&fakeClock{now: time.Now()}))
		if err != nil {
			b.Fatal(err)
		}
		scope := TenantScope("scope")
		root := key(scope, "root")
		if err := store.PutNode(ctx, node(root, nil, 10)); err != nil {
			b.Fatal(err)
		}
		for index := range entries {
			if err := store.RecordEstimate(ctx, Residency{
				Node: root, Endpoint: "pod-a", Generation: testGeneration,
				Tier:   fmt.Sprintf("tier-%d", index),
				Extent: Extent{Value: 10, Unit: ExtentUnitFramedBytes},
			}); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()
		err = store.PurgeEndpoint(ctx, "pod-a")
		b.StopTimer()
		cancel()
		if err != nil {
			b.Fatal(err)
		}
	}
}
