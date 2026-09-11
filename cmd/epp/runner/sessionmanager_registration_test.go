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

package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configloader "github.com/llm-d/llm-d-router/pkg/epp/config/loader"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/requestcontrol/dataproducer/sessionmanager"
)

func TestSessionManagerRegisteredAsExplicitAlphaPlugin(t *testing.T) {
	(&Runner{}).registerInTreePlugins()
	require.Contains(t, fwkplugin.Registry, sessionmanager.PluginType)
	assert.Equal(t, fwkplugin.StabilityAlpha, fwkplugin.GetPluginStability(sessionmanager.PluginType))
	for _, producerType := range fwkplugin.DefaultProducerRegistry {
		assert.NotEqual(t, sessionmanager.PluginType, producerType)
	}
}

func TestSessionManagerCorrelationConfigLoadsWithExplicitAlphaGate(t *testing.T) {
	(&Runner{}).registerInTreePlugins()
	configPath := filepath.Join("..", "..", "..", "deploy", "config", "epp-session-manager-v1-correlation.yaml")
	configBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)
	keyPath := writeSessionManagerTestKey(t)
	configBytes = bytes.ReplaceAll(
		configBytes,
		[]byte("/var/run/secrets/llm-d/session-manager-key"),
		[]byte(keyPath),
	)

	rawConfig, _, err := configloader.LoadRawConfig(configBytes, logr.Discard())
	require.NoError(t, err)
	handle := fwkplugin.NewEppHandle(t.Context(), nil)
	_, err = configloader.InstantiateAndConfigure(rawConfig, handle, logr.Discard())
	require.NoError(t, err)
	require.NotNil(t, handle.Plugin("sessions"))
	require.NotNil(t, handle.Plugin("session-statistics"))
	require.NotNil(t, handle.Plugin("correlation-cache"))

	require.Error(t, fwkplugin.ValidatePluginStability(handle, false))
	require.NoError(t, fwkplugin.ValidatePluginStability(handle, true))
}

func writeSessionManagerTestKey(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session-manager-key")
	// Unpadded base64url for 32 zero bytes.
	require.NoError(t, os.WriteFile(path, []byte("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), 0o600))
	return path
}
