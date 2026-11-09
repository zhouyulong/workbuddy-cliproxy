package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const endpointModels = upstreamBase + "/v3/config"

// Only reuse a discovery result for the same credential. Never let a temporary
// catalogue failure prevent CPA from binding the previously working models.
var discoveredModels sync.Map // SHA-256 of auth storage -> []pluginapi.ModelInfo

// Static models allow registration before login. Authenticated discovery must
// return the upstream catalogue so new models never require a plugin release.
func handleModelsForAuth(raw []byte) ([]byte, error) {
	var req pluginapi.AuthModelRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, fmt.Errorf("models request: %w", err)
	}
	sa, err := parseStored(req.StorageJSON)
	if err != nil {
		// Some hosts request models before credential storage is available.
		return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: wbModels()})
	}
	key := modelCacheKey(req.StorageJSON)
	fallback := wbModels()
	if cached, ok := discoveredModels.Load(key); ok {
		fallback = cached.([]pluginapi.ModelInfo)
	}
	// Bound the entire discovery, including all pages, so model registration
	// cannot consume the host's RPC timeout when the catalogue is unavailable.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	models, err := fetchModelsContext(ctx, sharedHTTPClient(), endpointModels, sa)
	if err != nil || len(models) == 0 {
		if err == nil {
			err = fmt.Errorf("upstream returned no usable models")
		}
		log.Printf("workbuddy: model discovery failed; using %d fallback models: %v", len(fallback), err)
		models = fallback
	} else {
		discoveredModels.Store(key, models)
	}
	return okEnvelope(pluginapi.ModelResponse{Provider: providerName, Models: models})
}

// Host model discovery and execution can serialize the same credentials with
// different key order or host-only fields. Hash the provider-owned shape so
// those equivalent representations share a cache without crossing accounts.
func modelCacheKey(storage []byte) [32]byte {
	if sa, err := parseStored(storage); err == nil {
		canonical, _ := json.Marshal(sa)
		return sha256.Sum256(canonical)
	}
	return sha256.Sum256(storage)
}

type upstreamReasoning struct {
	SupportedEfforts   []string `json:"supportedEfforts"`
	CanDisableThinking bool     `json:"canDisableThinking"`
}

type upstreamModel struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	OwnedBy             string            `json:"owned_by"`
	Created             int64             `json:"created"`
	Description         string            `json:"description"`
	ContextLength       int64             `json:"context_length"`
	MaxCompletionTokens int64             `json:"max_completion_tokens"`
	MaxInputTokens      int64             `json:"maxInputTokens"`
	MaxOutputTokens     int64             `json:"maxOutputTokens"`
	SupportsImages      bool              `json:"supportsImages"`
	Tags                []string          `json:"tags"`
	Disabled            bool              `json:"disabled"`
	SupportsReasoning   *bool             `json:"supportsReasoning"`
	OnlyReasoning       bool              `json:"onlyReasoning"`
	Reasoning           upstreamReasoning `json:"reasoning"`
}

type upstreamModelPage struct {
	Data    []upstreamModel `json:"data"`
	HasMore bool            `json:"has_more"`
	LastID  string          `json:"last_id"`
}

func fetchModels(client *http.Client, endpoint string, sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	return fetchModelsContext(context.Background(), client, endpoint, sa)
}

func fetchModelsContext(ctx context.Context, client *http.Client, endpoint string, sa *storedAuth) ([]pluginapi.ModelInfo, error) {
	models := make([]pluginapi.ModelInfo, 0)
	seen := make(map[string]bool)
	cursors := make(map[string]bool)
	known := make(map[string]pluginapi.ModelInfo)
	for _, m := range wbModels() {
		known[m.ID] = m
	}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		backendHeaders(req, sa)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		page, err := readModelPage(resp)
		if err != nil {
			return nil, err
		}
		for _, item := range page.Data {
			if !item.supportsChat() {
				continue
			}
			id := strings.TrimSpace(item.ID)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			m, ok := known[id]
			if !ok {
				m = pluginapi.ModelInfo{
					ID: id, Object: "model", OwnedBy: providerName,
					Name: id, DisplayName: id,
					SupportedGenerationMethods: []string{"chat"},
				}
			}
			m.UserDefined = false
			m.Created = item.Created
			m.Description = item.Description
			if item.Name != "" {
				m.DisplayName = item.Name
			}
			if item.OwnedBy != "" {
				m.OwnedBy = item.OwnedBy
			}
			if item.ContextLength > 0 {
				m.ContextLength = item.ContextLength
			}
			if item.MaxCompletionTokens > 0 {
				m.MaxCompletionTokens = item.MaxCompletionTokens
			}
			if item.MaxInputTokens > 0 {
				m.InputTokenLimit = item.MaxInputTokens
				m.ContextLength = item.MaxInputTokens
			}
			if item.MaxOutputTokens > 0 {
				m.OutputTokenLimit = item.MaxOutputTokens
				m.MaxCompletionTokens = item.MaxOutputTokens
			}
			if item.SupportsImages {
				m.SupportedInputModalities = []string{"text", "image"}
			}
			item.applyReasoningMetadata(&m)
			applyKnownModelProfile(&m)
			models = append(models, m)
		}
		if !page.HasMore {
			return models, nil
		}
		cursor := page.LastID
		if cursor == "" && len(page.Data) > 0 {
			cursor = page.Data[len(page.Data)-1].ID
		}
		if strings.TrimSpace(cursor) == "" || cursors[cursor] {
			return nil, fmt.Errorf("upstream model pagination did not advance")
		}
		cursors[cursor] = true
		u, err := url.Parse(endpoint)
		if err != nil {
			return nil, err
		}
		query := u.Query()
		query.Set("after", cursor)
		u.RawQuery = query.Encode()
		endpoint = u.String()
	}
}

func readModelPage(resp *http.Response) (upstreamModelPage, error) {
	defer resp.Body.Close()
	var page upstreamModelPage
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return page, fmt.Errorf("upstream model catalogue returned HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return page, fmt.Errorf("read model list: %w", err)
	}
	// CodeBuddy's /v3/config returns {code,msg,data:{models:[...]}}.
	// Also accept list responses for compatibility with model-list endpoints.
	var env struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return page, fmt.Errorf("decode model list: %w", err)
	}
	if env.Code != nil {
		if *env.Code != 0 {
			return page, fmt.Errorf("upstream model list code=%d", *env.Code)
		}
		if len(env.Data) > 0 && env.Data[0] == '{' {
			raw = env.Data
		}
	}
	var catalogue struct {
		Models []upstreamModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		return page, fmt.Errorf("decode model catalogue: %w", err)
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return page, fmt.Errorf("decode model list: %w", err)
	}
	if page.Data == nil {
		page.Data = catalogue.Models
	}
	if page.Data == nil {
		return page, fmt.Errorf("upstream model catalogue is missing models array")
	}
	return page, nil
}

// The executor implements chat completions. Image/video generation catalogue
// entries must not be advertised as executable chat models.
func (m upstreamModel) supportsChat() bool {
	if m.Disabled {
		return false
	}
	for _, tag := range m.Tags {
		if tag == "chat" {
			return true
		}
	}
	for _, tag := range m.Tags {
		switch tag {
		case "text-to-image", "image-to-image", "text-to-video", "image-to-video":
			return false
		}
	}
	return true
}

// Explicit capability metadata is authoritative for new models. When it is
// absent/incomplete, CPA's UserDefined compatibility path preserves the request
// rather than falsely asserting that the model cannot reason. No level list or
// default effort is invented for an unknown model.
func (m upstreamModel) applyReasoningMetadata(out *pluginapi.ModelInfo) {
	out.Thinking = nil
	out.UserDefined = true
	if m.SupportsReasoning != nil && !*m.SupportsReasoning && !m.OnlyReasoning {
		out.UserDefined = false
		params := make([]string, 0, len(out.SupportedParameters))
		for _, param := range out.SupportedParameters {
			if param != "reasoning_effort" {
				params = append(params, param)
			}
		}
		out.SupportedParameters = params
		return
	}
	levels := make([]string, 0)
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if hasEffort(m.Reasoning.SupportedEfforts, level) {
			levels = append(levels, level)
		}
	}
	if len(levels) > 0 {
		out.Thinking = &pluginapi.ThinkingSupport{Levels: levels, ZeroAllowed: m.Reasoning.CanDisableThinking && !m.OnlyReasoning}
		out.SupportedParameters = append(out.SupportedParameters, "reasoning_effort")
		out.UserDefined = false
	}
}
