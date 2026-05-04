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

// Package multimodalmetadata produces lightweight request-scoped multimodal
// hashes and placeholder counts for weighted encoder-cache affinity.
package multimodalmetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/llm-d/llm-d-kv-cache/pkg/tokenization"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	fwkrh "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requesthandling"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrmm "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/multimodal"
)

const (
	// ProducerType is the plugin type used in EPP config.
	ProducerType = "multimodal-metadata-data-producer"
	// ProducedKey is the request-scoped metadata key.
	ProducedKey = attrmm.RequestMetadataKey

	defaultHashMode               = "vllm"
	defaultUnsupportedModelPolicy = "unit-weight"
	defaultMetadataTimeout        = 150 * time.Millisecond
)

var _ requestcontrol.DataProducer = &Producer{}

type metadataClient interface {
	GetMultiModalMetadata(*tokenization.MultiModalMetadataRequest) (*tokenization.MultiModalMetadataResponse, error)
}

// Parameters configures the metadata producer.
type Parameters struct {
	ModelName                 string                          `json:"modelName"`
	TokenizerConfig           tokenization.UdsTokenizerConfig `json:"udsTokenizerConfig,omitempty"`
	HashMode                  string                          `json:"hashMode"`
	ProcessorKwargsJSON       string                          `json:"processorKwargsJson,omitempty"`
	UnsupportedModelPolicy    string                          `json:"unsupportedModelPolicy"`
	MetadataTimeout           string                          `json:"metadataTimeout"`
	AllowPreprocessFallback   bool                            `json:"allowPreprocessFallback"`
	AllowNonExactHashFallback bool                            `json:"allowNonExactHashFallback"`
}

// Factory creates a multimodal metadata producer.
func Factory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	params := Parameters{}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &params); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin - %w", ProducerType, err)
		}
	}
	p, err := New(handle.Context(), &params)
	if err != nil {
		return nil, err
	}
	return p.WithName(name), nil
}

// Producer attaches lightweight multimodal metadata to the request body.
type Producer struct {
	typedName                 plugin.TypedName
	modelName                 string
	hashMode                  string
	processorKwargsJSON       string
	unsupportedModelPolicy    string
	metadataTimeout           time.Duration
	allowPreprocessFallback   bool
	allowNonExactHashFallback bool
	client                    metadataClient
}

// New creates a Producer.
func New(ctx context.Context, params *Parameters) (*Producer, error) {
	if params == nil {
		params = &Parameters{}
	}
	if params.ModelName == "" {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'modelName' must be specified", ProducerType)
	}

	timeout := defaultMetadataTimeout
	if params.MetadataTimeout != "" {
		parsed, err := time.ParseDuration(params.MetadataTimeout)
		if err != nil {
			return nil, fmt.Errorf("invalid metadataTimeout %q: %w", params.MetadataTimeout, err)
		}
		timeout = parsed
	}

	tokenizerConfig := params.TokenizerConfig
	tokenizerConfig.SkipInitialize = !params.AllowPreprocessFallback
	tokenizerConfig.SkipWarmup = true
	client, err := tokenization.NewUdsTokenizer(ctx, &tokenizerConfig, params.ModelName)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize UDS metadata client for '%s' plugin - %w", ProducerType, err)
	}

	hashMode := params.HashMode
	if hashMode == "" {
		hashMode = defaultHashMode
	}
	policy := params.UnsupportedModelPolicy
	if policy == "" {
		policy = defaultUnsupportedModelPolicy
	}

	return &Producer{
		typedName:                 plugin.TypedName{Type: ProducerType},
		modelName:                 params.ModelName,
		hashMode:                  hashMode,
		processorKwargsJSON:       params.ProcessorKwargsJSON,
		unsupportedModelPolicy:    policy,
		metadataTimeout:           timeout,
		allowPreprocessFallback:   params.AllowPreprocessFallback,
		allowNonExactHashFallback: params.AllowNonExactHashFallback,
		client:                    client,
	}, nil
}

func (p *Producer) TypedName() plugin.TypedName {
	return p.typedName
}

func (p *Producer) WithName(name string) *Producer {
	p.typedName.Name = name
	return p
}

func (p *Producer) Produces() map[string]any {
	return map[string]any{ProducedKey: fwkrh.MultiModalMetadata{}}
}

func (p *Producer) Consumes() map[string]any {
	return nil
}

func (p *Producer) PrepareRequestData(ctx context.Context, request *scheduling.InferenceRequest, _ []scheduling.Endpoint) error {
	logger := log.FromContext(ctx).WithName(p.typedName.String()).V(logging.DEBUG)
	items := extractItems(request)
	if len(items) == 0 {
		logger.Info("No multimodal metadata items found")
		return nil
	}

	callDone := make(chan struct{})
	var resp *tokenization.MultiModalMetadataResponse
	var err error
	go func() {
		defer close(callDone)
		resp, err = p.client.GetMultiModalMetadata(&tokenization.MultiModalMetadataRequest{
			ModelName:               p.modelName,
			Items:                   items,
			ProcessorKwargsJSON:     p.processorKwargsJSON,
			AllowPreprocessFallback: p.allowPreprocessFallback,
			HashMode:                p.hashMode,
		})
	}()

	select {
	case <-ctx.Done():
		logger.Info("MM metadata request canceled", "policy", p.unsupportedModelPolicy, "error", ctx.Err())
		return nil
	case <-time.After(p.metadataTimeout):
		logger.Info("MM metadata request timed out", "timeout", p.metadataTimeout.String(), "policy", p.unsupportedModelPolicy)
		return nil
	case <-callDone:
	}
	if err != nil {
		logger.Info("MM metadata request failed", "policy", p.unsupportedModelPolicy, "error", err)
		return nil
	}

	metadata := &fwkrh.MultiModalMetadata{Items: make([]fwkrh.MultiModalItemMetadata, 0, len(resp.Items))}
	for _, item := range resp.Items {
		metadata.Items = append(metadata.Items, fwkrh.MultiModalItemMetadata{
			Modality:              fwkrh.Modality(item.Modality),
			Hash:                  item.Hash,
			PlaceholderCount:      item.PlaceholderCount,
			Width:                 item.Width,
			Height:                item.Height,
			Source:                item.Method,
			ExactHash:             item.ExactHash || p.allowNonExactHashFallback,
			ExactPlaceholderCount: item.ExactPlaceholderCount,
			FallbackUsed:          strings.Contains(item.Method, "fallback"),
		})
	}
	if request != nil && request.Body != nil {
		request.Body.MultiModalMetadata = metadata
	}
	logger.Info("Attached MM metadata", "items", len(metadata.Items), "hashMode", p.hashMode)
	return nil
}

func extractItems(request *scheduling.InferenceRequest) []tokenization.MultiModalMetadataItemRequest {
	if request == nil || request.Body == nil {
		return nil
	}
	items := make([]tokenization.MultiModalMetadataItemRequest, 0)
	if request.Body.ChatCompletions != nil {
		for _, message := range request.Body.ChatCompletions.Messages {
			for _, block := range message.Content.Structured {
				items = appendBlockItem(items, block)
			}
		}
		return items
	}
	if request.Body.Responses != nil {
		walkAny(request.Body.Responses.Input, &items)
		return items
	}
	if request.Body.Conversations != nil {
		walkAny(request.Body.Conversations.Items, &items)
		return items
	}
	if request.Body.Payload != nil && request.Body.Payload.IsParsed() {
		walkAny(request.Body.Payload, &items)
	}
	return items
}

func appendBlockItem(items []tokenization.MultiModalMetadataItemRequest, block fwkrh.ContentBlock) []tokenization.MultiModalMetadataItemRequest {
	if block.ImageURL.Url != "" {
		return append(items, tokenization.MultiModalMetadataItemRequest{
			Modality: string(fwkrh.ModalityImage),
			URL:      block.ImageURL.Url,
		})
	}
	return items
}

func walkAny(value any, items *[]tokenization.MultiModalMetadataItemRequest) {
	switch typed := value.(type) {
	case fwkrh.PayloadMap:
		for k, v := range typed {
			walkNamedValue(k, v, items)
		}
	case map[string]any:
		for k, v := range typed {
			walkNamedValue(k, v, items)
		}
	case []any:
		for _, item := range typed {
			walkAny(item, items)
		}
	case []fwkrh.ConversationItem:
		for _, item := range typed {
			walkAny(item.Content, items)
		}
	case fwkrh.Content:
		for _, block := range typed.Structured {
			*items = appendBlockItem(*items, block)
		}
	case []fwkrh.ContentBlock:
		for _, block := range typed {
			*items = appendBlockItem(*items, block)
		}
	}
}

func walkNamedValue(name string, value any, items *[]tokenization.MultiModalMetadataItemRequest) {
	if strings.EqualFold(name, "image_url") {
		if url, uuid := imageIdentifier(value); url != "" || uuid != "" {
			*items = append(*items, tokenization.MultiModalMetadataItemRequest{
				Modality: string(fwkrh.ModalityImage),
				URL:      url,
				UUID:     uuid,
			})
			return
		}
	}
	walkAny(value, items)
}

func imageIdentifier(value any) (string, string) {
	switch typed := value.(type) {
	case string:
		return typed, ""
	case map[string]any:
		url, _ := typed["url"].(string)
		uuid, _ := typed["uuid"].(string)
		return url, uuid
	default:
		return "", ""
	}
}
