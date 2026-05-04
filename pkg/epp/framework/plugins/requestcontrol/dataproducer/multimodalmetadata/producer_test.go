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

package multimodalmetadata

import (
	"context"
	"testing"
	"time"

	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

type mockMetadataClient struct {
	resp    *tokenization.MultiModalMetadataResponse
	err     error
	request *tokenization.MultiModalMetadataRequest
	delay   time.Duration
}

func (m *mockMetadataClient) GetMultiModalMetadata(req *tokenization.MultiModalMetadataRequest) (*tokenization.MultiModalMetadataResponse, error) {
	m.request = req
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	return m.resp, m.err
}

func TestPrepareRequestDataAttachesMetadata(t *testing.T) {
	client := &mockMetadataClient{resp: &tokenization.MultiModalMetadataResponse{
		Success: true,
		Items: []tokenization.MultiModalMetadataItem{{
			Modality:              "image",
			Hash:                  "hash-a",
			PlaceholderCount:      64,
			Width:                 224,
			Height:                224,
			ExactHash:             true,
			ExactPlaceholderCount: true,
			Method:                "vllm-hash+lightweight-count",
		}},
	}}
	producer := &Producer{
		typedName:       pluginName(),
		modelName:       "test-model",
		hashMode:        "vllm",
		metadataTimeout: time.Second,
		client:          client,
	}
	request := chatRequest("https://example.com/a.png")

	require.NoError(t, producer.PrepareRequestData(context.Background(), request, nil))

	require.NotNil(t, request.Body.MultiModalMetadata)
	require.Len(t, request.Body.MultiModalMetadata.Items, 1)
	assert.Equal(t, "hash-a", request.Body.MultiModalMetadata.Items[0].Hash)
	assert.Equal(t, 64, request.Body.MultiModalMetadata.Items[0].PlaceholderCount)
	require.NotNil(t, client.request)
	require.Len(t, client.request.Items, 1)
	assert.Equal(t, "https://example.com/a.png", client.request.Items[0].URL)
}

func TestPrepareRequestDataTimeoutFallsBackWithoutMetadata(t *testing.T) {
	client := &mockMetadataClient{delay: 50 * time.Millisecond}
	producer := &Producer{
		typedName:       pluginName(),
		modelName:       "test-model",
		hashMode:        "vllm",
		metadataTimeout: time.Millisecond,
		client:          client,
	}
	request := chatRequest("https://example.com/a.png")

	require.NoError(t, producer.PrepareRequestData(context.Background(), request, nil))
	assert.Nil(t, request.Body.MultiModalMetadata)
}

func chatRequest(url string) *scheduling.InferenceRequest {
	return &scheduling.InferenceRequest{
		Body: &fwkrh.InferenceRequestBody{
			ChatCompletions: &fwkrh.ChatCompletionsRequest{
				Messages: []fwkrh.Message{{
					Role: "user",
					Content: fwkrh.Content{Structured: []fwkrh.ContentBlock{
						{Type: "image_url", ImageURL: fwkrh.ImageBlock{Url: url}},
					}},
				}},
			},
		},
	}
}

func pluginName() plugin.TypedName {
	return plugin.TypedName{Type: ProducerType}
}
