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

package runner

import (
	"testing"

	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	sessionstateplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/sessionstate"

	"github.com/stretchr/testify/require"
)

func TestRegisterInTreePluginsRegistersAlphaSessionStateProvider(t *testing.T) {
	runner := NewRunner()
	runner.registerInTreePlugins()

	factory, exists := fwkplugin.Registry[sessionstateplugin.PluginType]
	require.True(t, exists)
	require.NotNil(t, factory)
	require.Equal(t, fwkplugin.StabilityAlpha,
		fwkplugin.RegistryMetadata[sessionstateplugin.PluginType].Stability)

	handle := fwkplugin.NewEppHandle(t.Context(), nil)
	created, err := factory("central-session-state", nil, handle)
	require.NoError(t, err)
	require.Equal(t, sessionstateplugin.PluginType, created.TypedName().Type)
	require.Equal(t, "central-session-state", created.TypedName().Name)
}
