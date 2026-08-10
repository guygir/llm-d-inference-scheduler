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

	storeapi "github.com/llm-d/llm-d-router/pkg/sessionstate"
)

const defaultEndpointReconcileInterval = 2 * time.Minute

type config struct {
	NodeCapacity              *int   `json:"nodeCapacity,omitempty"`
	AliasCapacity             *int   `json:"aliasCapacity,omitempty"`
	ResidencyCapacity         *int   `json:"residencyCapacity,omitempty"`
	MaxTipsPerEndpoint        *int   `json:"maxTipsPerEndpoint,omitempty"`
	MaxAncestryDepth          *int   `json:"maxAncestryDepth,omitempty"`
	MaxCoverageSteps          *int   `json:"maxCoverageSteps,omitempty"`
	NodeTTL                   string `json:"nodeTTL,omitempty"`
	AliasTTL                  string `json:"aliasTTL,omitempty"`
	EstimateTTL               string `json:"estimateTTL,omitempty"`
	CleanupInterval           string `json:"cleanupInterval,omitempty"`
	EndpointReconcileInterval string `json:"endpointReconcileInterval,omitempty"`
}

func (c config) storeConfig() (storeapi.Config, time.Duration, error) {
	result := storeapi.DefaultConfig()
	if c.NodeCapacity != nil {
		result.NodeCapacity = *c.NodeCapacity
	}
	if c.AliasCapacity != nil {
		result.AliasCapacity = *c.AliasCapacity
	}
	if c.ResidencyCapacity != nil {
		result.ResidencyCapacity = *c.ResidencyCapacity
	}
	if c.MaxTipsPerEndpoint != nil {
		result.MaxTipsPerEndpoint = *c.MaxTipsPerEndpoint
	}
	if c.MaxAncestryDepth != nil {
		result.MaxAncestryDepth = *c.MaxAncestryDepth
	}
	if c.MaxCoverageSteps != nil {
		result.MaxCoverageSteps = *c.MaxCoverageSteps
	}

	var err error
	if result.NodeTTL, err = parseDuration("nodeTTL", c.NodeTTL, result.NodeTTL); err != nil {
		return storeapi.Config{}, 0, err
	}
	if result.AliasTTL, err = parseDuration("aliasTTL", c.AliasTTL, result.AliasTTL); err != nil {
		return storeapi.Config{}, 0, err
	}
	if result.EstimateTTL, err = parseDuration("estimateTTL", c.EstimateTTL, result.EstimateTTL); err != nil {
		return storeapi.Config{}, 0, err
	}
	if result.CleanupInterval, err = parseDuration(
		"cleanupInterval", c.CleanupInterval, result.CleanupInterval,
	); err != nil {
		return storeapi.Config{}, 0, err
	}
	reconcileInterval, err := parseDuration(
		"endpointReconcileInterval",
		c.EndpointReconcileInterval,
		defaultEndpointReconcileInterval,
	)
	if err != nil {
		return storeapi.Config{}, 0, err
	}
	if err := result.Validate(); err != nil {
		return storeapi.Config{}, 0, err
	}
	return result, reconcileInterval, nil
}

func parseDuration(field, value string, fallback time.Duration) (time.Duration, error) {
	if value == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", field)
	}
	return duration, nil
}
