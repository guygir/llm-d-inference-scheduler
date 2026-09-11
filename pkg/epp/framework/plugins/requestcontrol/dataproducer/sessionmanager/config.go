/*
Copyright 2026 The llm-d Authors.

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

package sessionmanager

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultBindingTTL      = 5 * time.Minute
	defaultMaxBindings     = 100_000
	maxBindingsLimit       = 1_000_000
	maxConfigurationLength = 1024
	sessionManagerKeyBytes = 32
)

type namespaceConfig struct {
	Endpoint       string `json:"endpoint"`
	ModelName      string `json:"modelName"`
	GroupIdx       *int   `json:"groupIdx,omitempty"`
	CacheNamespace string `json:"cacheNamespace"`
}

// Config configures the session-manager plugin.
type Config struct {
	DeploymentID            string            `json:"deploymentID"`
	HMACKeyFile             string            `json:"hmacKeyFile"`
	TokenProducer           string            `json:"tokenProducer,omitempty"`
	EventCorrelationEnabled bool              `json:"eventCorrelationEnabled,omitempty"`
	BindingTTL              string            `json:"bindingTTL,omitempty"`
	MaxBindings             int               `json:"maxBindings,omitempty"`
	CacheNamespaces         []namespaceConfig `json:"cacheNamespaces,omitempty"`
}

type resolvedConfig struct {
	deploymentID            string
	hmacKey                 []byte
	tokenProducer           string
	eventCorrelationEnabled bool
	bindingTTL              time.Duration
	maxBindings             int
	cacheNamespaces         map[namespaceKey]string
}

func (c Config) resolve() (resolvedConfig, error) {
	cfg := resolvedConfig{
		deploymentID:            c.DeploymentID,
		tokenProducer:           strings.TrimSpace(c.TokenProducer),
		eventCorrelationEnabled: c.EventCorrelationEnabled,
		bindingTTL:              defaultBindingTTL,
		maxBindings:             defaultMaxBindings,
		cacheNamespaces:         make(map[namespaceKey]string, len(c.CacheNamespaces)),
	}
	if strings.TrimSpace(cfg.deploymentID) == "" ||
		cfg.deploymentID != strings.TrimSpace(cfg.deploymentID) ||
		len(cfg.deploymentID) > maxConfigurationLength {
		return resolvedConfig{}, errors.New("deploymentID must be non-empty, have no surrounding whitespace, and be at most 1024 bytes")
	}
	keyPath := strings.TrimSpace(c.HMACKeyFile)
	if keyPath == "" {
		return resolvedConfig{}, errors.New("hmacKeyFile is required")
	}
	encoded, err := os.ReadFile(keyPath)
	if err != nil {
		return resolvedConfig{}, fmt.Errorf("read hmacKeyFile: %w", err)
	}
	cfg.hmacKey, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(cfg.hmacKey) != sessionManagerKeyBytes {
		return resolvedConfig{}, errors.New("hmacKeyFile must contain unpadded base64url for exactly 32 bytes")
	}
	if c.BindingTTL != "" {
		cfg.bindingTTL, err = time.ParseDuration(c.BindingTTL)
		if err != nil {
			return resolvedConfig{}, fmt.Errorf("parse bindingTTL: %w", err)
		}
	}
	if cfg.bindingTTL <= 0 || cfg.bindingTTL > time.Hour {
		return resolvedConfig{}, errors.New("bindingTTL must be greater than zero and at most 1h")
	}
	if c.MaxBindings != 0 {
		cfg.maxBindings = c.MaxBindings
	}
	if cfg.maxBindings <= 0 || cfg.maxBindings > maxBindingsLimit {
		return resolvedConfig{}, fmt.Errorf("maxBindings must be between 1 and %d", maxBindingsLimit)
	}
	if cfg.eventCorrelationEnabled && cfg.tokenProducer == "" {
		return resolvedConfig{}, errors.New("tokenProducer is required when eventCorrelationEnabled is true")
	}
	for _, item := range c.CacheNamespaces {
		endpoint := strings.TrimSpace(item.Endpoint)
		model := strings.TrimSpace(item.ModelName)
		namespace := strings.TrimSpace(item.CacheNamespace)
		if endpoint == "" || model == "" || namespace == "" ||
			len(endpoint) > maxConfigurationLength ||
			len(model) > maxConfigurationLength || len(namespace) > maxConfigurationLength {
			return resolvedConfig{}, errors.New("cacheNamespaces entries require bounded endpoint, modelName, and cacheNamespace")
		}
		key := makeNamespaceKey(endpoint, model, item.GroupIdx)
		if _, exists := cfg.cacheNamespaces[key]; exists {
			return resolvedConfig{}, fmt.Errorf(
				"duplicate cache namespace mapping for endpoint %q, model %q, and group %s",
				endpoint,
				model,
				key.group,
			)
		}
		cfg.cacheNamespaces[key] = namespace
	}
	if cfg.eventCorrelationEnabled && len(cfg.cacheNamespaces) == 0 {
		return resolvedConfig{}, errors.New("cacheNamespaces is required when eventCorrelationEnabled is true")
	}
	return cfg, nil
}
