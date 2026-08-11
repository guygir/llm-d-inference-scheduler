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
	"fmt"
	"time"
)

const (
	defaultNodeCapacity       = 100_000
	defaultAliasCapacity      = 100_000
	defaultResidencyCapacity  = 100_000
	defaultMaxTipsPerEndpoint = 32
	defaultMaxAncestryDepth   = 8192
	defaultMaxCoverageSteps   = 1_000_000
	defaultNodeTTL            = time.Hour
	defaultAliasTTL           = time.Hour
	defaultEstimateTTL        = 2 * time.Minute
	defaultCleanupInterval    = time.Minute
	maxEntryCapacity          = 1_000_000
	maxTipsPerEndpointLimit   = 4096
	maxAncestryDepthLimit     = 65_536
	maxCoverageStepsLimit     = 10_000_000
	maxEntryTTL               = 7 * 24 * time.Hour
)

// Config bounds the in-memory store. Defaults are intentionally configurable
// prototype values and must be tuned from trace/load evidence.
type Config struct {
	NodeCapacity       int
	AliasCapacity      int
	ResidencyCapacity  int
	MaxTipsPerEndpoint int
	MaxAncestryDepth   int
	MaxCoverageSteps   int
	NodeTTL            time.Duration
	AliasTTL           time.Duration
	EstimateTTL        time.Duration
	CleanupInterval    time.Duration
}

// DefaultConfig returns the initial bounded store configuration.
func DefaultConfig() Config {
	return Config{
		NodeCapacity:       defaultNodeCapacity,
		AliasCapacity:      defaultAliasCapacity,
		ResidencyCapacity:  defaultResidencyCapacity,
		MaxTipsPerEndpoint: defaultMaxTipsPerEndpoint,
		MaxAncestryDepth:   defaultMaxAncestryDepth,
		MaxCoverageSteps:   defaultMaxCoverageSteps,
		NodeTTL:            defaultNodeTTL,
		AliasTTL:           defaultAliasTTL,
		EstimateTTL:        defaultEstimateTTL,
		CleanupInterval:    defaultCleanupInterval,
	}
}

// Validate checks that every bound and duration is usable.
func (c Config) Validate() error {
	if c.NodeCapacity <= 0 {
		return fmt.Errorf("%w: node capacity must be positive", ErrInvalidConfig)
	}
	if c.NodeCapacity > maxEntryCapacity {
		return fmt.Errorf("%w: node capacity exceeds safety limit", ErrInvalidConfig)
	}
	if c.AliasCapacity <= 0 {
		return fmt.Errorf("%w: alias capacity must be positive", ErrInvalidConfig)
	}
	if c.AliasCapacity > maxEntryCapacity {
		return fmt.Errorf("%w: alias capacity exceeds safety limit", ErrInvalidConfig)
	}
	if c.ResidencyCapacity <= 0 {
		return fmt.Errorf("%w: residency capacity must be positive", ErrInvalidConfig)
	}
	if c.ResidencyCapacity > maxEntryCapacity {
		return fmt.Errorf("%w: residency capacity exceeds safety limit", ErrInvalidConfig)
	}
	if c.MaxTipsPerEndpoint <= 0 {
		return fmt.Errorf("%w: max tips per endpoint must be positive", ErrInvalidConfig)
	}
	if c.MaxTipsPerEndpoint > maxTipsPerEndpointLimit {
		return fmt.Errorf("%w: max tips per endpoint exceeds safety limit", ErrInvalidConfig)
	}
	if c.MaxAncestryDepth <= 0 {
		return fmt.Errorf("%w: max ancestry depth must be positive", ErrInvalidConfig)
	}
	if c.MaxAncestryDepth > maxAncestryDepthLimit {
		return fmt.Errorf("%w: max ancestry depth exceeds safety limit", ErrInvalidConfig)
	}
	if c.MaxCoverageSteps <= 0 {
		return fmt.Errorf("%w: max coverage steps must be positive", ErrInvalidConfig)
	}
	if c.MaxCoverageSteps > maxCoverageStepsLimit {
		return fmt.Errorf("%w: max coverage steps exceeds safety limit", ErrInvalidConfig)
	}
	if c.NodeTTL <= 0 {
		return fmt.Errorf("%w: node TTL must be positive", ErrInvalidConfig)
	}
	if c.AliasTTL <= 0 {
		return fmt.Errorf("%w: alias TTL must be positive", ErrInvalidConfig)
	}
	if c.EstimateTTL <= 0 {
		return fmt.Errorf("%w: estimate TTL must be positive", ErrInvalidConfig)
	}
	if c.NodeTTL > maxEntryTTL || c.AliasTTL > maxEntryTTL || c.EstimateTTL > maxEntryTTL {
		return fmt.Errorf("%w: entry TTL exceeds safety limit", ErrInvalidConfig)
	}
	if c.CleanupInterval <= 0 {
		return fmt.Errorf("%w: cleanup interval must be positive", ErrInvalidConfig)
	}
	if c.NodeTTL < c.AliasTTL {
		return fmt.Errorf("%w: node TTL must not be shorter than alias TTL", ErrInvalidConfig)
	}
	if c.NodeTTL < c.EstimateTTL {
		return fmt.Errorf("%w: node TTL must not be shorter than estimate TTL", ErrInvalidConfig)
	}
	return nil
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
