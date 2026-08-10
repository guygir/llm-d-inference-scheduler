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

// Package sessionstate defines the logical session-state contract shared by
// session identity, affinity, and future KV-event consumers.
package sessionstate

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

// TenantScope is the complete router comparison scope. Callers construct it
// from trusted tenant isolation, resolved model, effective cache salt, and
// framing version. It must never be derived from a client session ID alone.
type TenantScope string

// NodeID identifies one immutable SHA-256 content-derived lineage node.
type NodeID [sha256.Size]byte

// NodeKey uniquely identifies a lineage node inside one comparison scope.
type NodeKey struct {
	TenantScope TenantScope
	NodeID      NodeID
}

// SessionRef carries optional policy/lifecycle metadata. It is not part of
// content identity.
type SessionRef struct {
	TenantScope TenantScope
	SessionID   string
}

// AliasKind distinguishes protocol identifiers with different semantics.
type AliasKind string

const (
	AliasKindResponse       AliasKind = "response"
	AliasKindPromptCacheKey AliasKind = "prompt_cache_key"
	AliasKindSession        AliasKind = "session"
	AliasKindConversation   AliasKind = "conversation"
	AliasKindFork           AliasKind = "fork"
	AliasKindSubAgent       AliasKind = "sub_agent"
)

// AliasKey is a typed external identifier scoped to one tenant/cache domain.
type AliasKey struct {
	TenantScope TenantScope
	Kind        AliasKind
	Value       string
}

// AliasTarget is the logical node and optional session metadata reached by an
// external identifier.
type AliasTarget struct {
	Node    NodeKey
	Session *SessionRef
}

// ExtentUnit prevents quantities from incompatible coordinate systems from
// being compared.
type ExtentUnit string

const (
	ExtentUnitFramedBytes ExtentUnit = "framed_bytes"
	ExtentUnitModelTokens ExtentUnit = "model_tokens"
	ExtentUnitTokenBlocks ExtentUnit = "token_blocks"
)

// Extent is an amount in one explicit coordinate system.
type Extent struct {
	Value uint64
	Unit  ExtentUnit
}

// Node is one immutable lineage position.
type Node struct {
	Key              NodeKey
	Parent           *NodeKey
	CumulativeExtent Extent
}

// EvidenceSource describes how a residency fact was learned.
type EvidenceSource string

const (
	EvidenceSourceEstimate  EvidenceSource = "estimated"
	EvidenceSourceConfirmed EvidenceSource = "confirmed"
)

// EndpointGeneration identifies one lifecycle generation of a named endpoint.
type EndpointGeneration uint64

// Residency describes one endpoint/tier observation for a logical node.
type Residency struct {
	Node           NodeKey
	Endpoint       string
	Generation     EndpointGeneration
	Tier           string
	Extent         Extent
	Source         EvidenceSource
	ObservedAt     time.Time
	ExpiresAt      time.Time
	PublisherEpoch string
}

// EndpointCoverage is the deepest compatible extent found for one endpoint and tier.
type EndpointCoverage struct {
	Endpoint    string
	Tier        string
	MatchedNode *NodeKey
	Covered     Extent
	Requested   Extent
	Fraction    float64
	Source      EvidenceSource
}

// Stats reports bounded store cardinalities without exposing identifiers.
type Stats struct {
	Nodes       int `json:"nodes"`
	Aliases     int `json:"aliases"`
	Residencies int `json:"residencies"`
	Endpoints   int `json:"endpoints"`
}

// Store is the shared logical-state contract. Implementations must be safe for
// concurrent callers.
type Store interface {
	PutNode(ctx context.Context, node Node) error
	PutAlias(ctx context.Context, key AliasKey, target AliasTarget, ttl time.Duration) error
	ResolveAlias(ctx context.Context, key AliasKey) (AliasTarget, bool)
	RecordEstimate(ctx context.Context, residency Residency) error
	Coverage(ctx context.Context, tip NodeKey, requested Extent, candidates []string) ([]EndpointCoverage, error)
	PurgeEndpoint(ctx context.Context, endpoint string) error
	Expire(now time.Time)
	Stats() Stats
}

var (
	ErrInvalidConfig       = errors.New("invalid session-state configuration")
	ErrInvalidKey          = errors.New("invalid session-state key")
	ErrNodeNotFound        = errors.New("session-state node not found")
	ErrNodeConflict        = errors.New("session-state node conflicts with existing immutable node")
	ErrExtentUnit          = errors.New("session-state extent units do not match")
	ErrAncestryCycle       = errors.New("session-state ancestry cycle")
	ErrInvalidAlias        = errors.New("invalid session-state alias")
	ErrInvalidNode         = errors.New("invalid session-state node")
	ErrInvalidRecord       = errors.New("invalid session-state residency")
	ErrEndpointUnavailable = errors.New("session-state endpoint is unavailable")
	ErrAncestryTooDeep     = errors.New("session-state ancestry exceeds configured depth")
	ErrCoverageBudget      = errors.New("session-state coverage work budget exceeded")
)
