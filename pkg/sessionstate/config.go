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
	if c.AliasCapacity <= 0 {
		return fmt.Errorf("%w: alias capacity must be positive", ErrInvalidConfig)
	}
	if c.ResidencyCapacity <= 0 {
		return fmt.Errorf("%w: residency capacity must be positive", ErrInvalidConfig)
	}
	if c.MaxTipsPerEndpoint <= 0 {
		return fmt.Errorf("%w: max tips per endpoint must be positive", ErrInvalidConfig)
	}
	if c.MaxAncestryDepth <= 0 {
		return fmt.Errorf("%w: max ancestry depth must be positive", ErrInvalidConfig)
	}
	if c.MaxCoverageSteps <= 0 {
		return fmt.Errorf("%w: max coverage steps must be positive", ErrInvalidConfig)
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
	if c.CleanupInterval <= 0 {
		return fmt.Errorf("%w: cleanup interval must be positive", ErrInvalidConfig)
	}
	return nil
}

type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
