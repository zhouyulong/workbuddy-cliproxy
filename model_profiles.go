package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Channel profiles supplied on 2026-11-09. These describe CodeBuddy's channel,
// not the public APIs of the underlying model vendors. Selection defaults and
// credit multipliers are reference metadata, NOT SDK limits or billing rules.
// Hy3's window/output/pricing were not supplied and remain upstream-controlled.
type modelProfile struct {
	ID, Name                string
	Aliases                 []string
	Efforts                 []string
	DefaultEffort           string
	InputLimit, OutputLimit int64
	ContextWindows          []int64
	DefaultContext          int64
	CreditMultiplier        string
}

var channelModelProfiles = []modelProfile{
	{ID: "space-bunny", Name: "Space-Bunny", Efforts: []string{"low", "medium", "high", "xhigh", "max"}, DefaultEffort: "max", InputLimit: 1000000, OutputLimit: 128000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 1000000, CreditMultiplier: "0.08"},
	{ID: "minimax-m3.1-flash", Name: "MiniMax-M3.1-Flash", Efforts: []string{"low", "medium", "high", "xhigh", "max"}, DefaultEffort: "max", InputLimit: 1000000, OutputLimit: 128000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.08"},
	{ID: "step-5-preview", Name: "Step-5-Preview", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 64000, ContextWindows: []int64{300000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.33"},
	// K3-2 is present in the native WorkBuddy catalogue; K3-1 is present in
	// the local CPA catalogue. Match discovered IDs/names; do not rewrite IDs.
	{ID: "kimi-k3-2", Name: "Kimi-K3", Aliases: []string{"kimi-k3-1", "kimi-k3"}, Efforts: []string{"low", "high", "xhigh"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 32000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "1.62"},
	{ID: "glm-5.3", Name: "GLM-5.3", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 64000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.69"},
	// The native catalogue gives the exact integer behind the rounded "131K".
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 131072, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.08"},
	{ID: "kimi-k2.8-preview", Name: "Kimi-K2.8-Preview", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 64000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.77"},
	{ID: "kimi-k3.1-pro", Name: "Kimi-K3.1-Pro", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 32000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.97"},
	{ID: "deepseek-v4.1-flash", Name: "DeepSeek-V4.1-Flash", Efforts: []string{"low", "high", "max"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 128000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.11"},
	{ID: "glm-5.2", Name: "GLM-5.2", Efforts: []string{"high", "xhigh"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 64000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.79"},
	{ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash", Efforts: []string{"high", "xhigh"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 50000, ContextWindows: []int64{300000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.17"},
	{ID: "deepseek-v4-pro", Name: "DeepSeek-V4-Pro", Efforts: []string{"high", "xhigh"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 128000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.51"},
	{ID: "hy3", Name: "Hy3", Aliases: []string{"hy3-x"}, Efforts: []string{"low", "high"}, DefaultEffort: "high"},
	{ID: "hy4-preview", Name: "Hy4 preview", Efforts: []string{"high"}, DefaultEffort: "high", InputLimit: 960000, OutputLimit: 64000, ContextWindows: []int64{300000, 600000, 960000}, DefaultContext: 300000, CreditMultiplier: "0.29"},
	{ID: "hy4", Name: "Hy4", Efforts: []string{"high"}, DefaultEffort: "high", InputLimit: 960000, OutputLimit: 64000, ContextWindows: []int64{300000, 600000, 960000}, DefaultContext: 300000, CreditMultiplier: "0.00"},
	{ID: "mimo-v2.6-pro", Name: "Xiaomi MiMo-V2.6-Pro", Efforts: []string{"high"}, DefaultEffort: "high", InputLimit: 1000000, OutputLimit: 128000, ContextWindows: []int64{300000, 600000, 1000000}, DefaultContext: 300000, CreditMultiplier: "0.13"},
	// Keep the old preview IDs usable without inventing their current effort
	// range. The previous family override supplied high even when omitted.
	{ID: "hy3-preview", Name: "Hy3 Preview", DefaultEffort: "high"},
	{ID: "hy3-preview-agent", Name: "Hy3 Preview Agent", DefaultEffort: "high"},
}

func profileName(s string) string {
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func findModelProfile(names ...string) *modelProfile {
	for _, name := range names {
		key := profileName(name)
		if key == "" {
			continue
		}
		for i := range channelModelProfiles {
			p := &channelModelProfiles[i]
			if key == profileName(p.ID) || key == profileName(p.Name) {
				return p
			}
			for _, alias := range p.Aliases {
				if key == profileName(alias) {
					return p
				}
			}
		}
	}
	return nil
}

func applyKnownModelProfile(m *pluginapi.ModelInfo) bool {
	p := findModelProfile(m.ID, m.DisplayName)
	if p == nil {
		return false
	}
	if len(p.Efforts) > 0 {
		m.Thinking = &pluginapi.ThinkingSupport{Levels: append([]string(nil), p.Efforts...)}
		m.UserDefined = false
	} else if m.Thinking == nil && !m.UserDefined {
		// A default-only policy must not override an explicit upstream
		// non-reasoning declaration. Unknown metadata already uses the
		// UserDefined compatibility path; keep that distinct from false.
		return false
	}
	if !hasEffort(m.SupportedParameters, "reasoning_effort") {
		m.SupportedParameters = append(m.SupportedParameters, "reasoning_effort")
	}
	if p.InputLimit > 0 {
		m.InputTokenLimit = p.InputLimit
		m.ContextLength = p.InputLimit
	}
	if p.OutputLimit > 0 {
		m.OutputTokenLimit = p.OutputLimit
		m.MaxCompletionTokens = p.OutputLimit
	}
	return true
}

func hasEffort(levels []string, effort string) bool {
	for _, level := range levels {
		if level == effort {
			return true
		}
	}
	return false
}

// Reuse the credential-scoped discovery cache for channel profiles matched by
// display name (the real upstream ID may differ from the model's UI name).
func requestModelProfile(req pluginapi.ExecutorRequest, model string) (*modelProfile, []string) {
	profile := findModelProfile(model)
	if profile != nil && len(profile.Efforts) > 0 {
		return profile, profile.Efforts
	}
	if cached, ok := discoveredModels.Load(modelCacheKey(req.StorageJSON)); ok {
		for _, m := range cached.([]pluginapi.ModelInfo) {
			if m.ID != model {
				continue
			}
			if m.Thinking == nil && !m.UserDefined {
				// A non-nil empty set means explicitly no reasoning support;
				// nil means unknown capability and permits compatibility.
				return nil, []string{}
			}
			if profile == nil {
				profile = findModelProfile(m.DisplayName)
			}
			if profile != nil && len(profile.Efforts) > 0 {
				return profile, profile.Efforts
			}
			if m.Thinking != nil {
				levels := append([]string(nil), m.Thinking.Levels...)
				if m.Thinking.ZeroAllowed && !hasEffort(levels, "none") {
					levels = append(levels, "none")
				}
				return profile, levels
			}
		}
	}
	return profile, nil
}

// Prepare only CodeBuddy's named effort and summary controls. CPA translates
// Responses to chat-completions before calling this executor. RawMessage keeps
// unrelated JSON numbers/fields unchanged when defaults are added.
func prepareReasoningForUpstream(payload []byte, req pluginapi.ExecutorRequest) ([]byte, error) {
	var body map[string]json.RawMessage
	if err := json.Unmarshal(payload, &body); err != nil || body == nil {
		return nil, fmt.Errorf("invalid chat completion payload")
	}
	model := req.Model
	if model == "" {
		_ = json.Unmarshal(body["model"], &model)
	}
	profile, levels := requestModelProfile(req, model)
	effort := ""
	if raw, ok := body["reasoning_effort"]; ok {
		if err := json.Unmarshal(raw, &effort); err != nil {
			return nil, fmt.Errorf("reasoning_effort must be a string")
		}
		effort = strings.ToLower(strings.TrimSpace(effort))
	}
	if effort == "" && profile != nil {
		effort = profile.DefaultEffort
	}
	if effort != "" && levels != nil && !hasEffort(levels, effort) {
		if len(levels) == 0 {
			return nil, fmt.Errorf("model %s does not support reasoning_effort", model)
		}
		return nil, fmt.Errorf("model %s does not support reasoning_effort %q; supported: %s", model, effort, strings.Join(levels, ", "))
	}
	changed := false
	if effort != "" {
		encoded, _ := json.Marshal(effort)
		if string(body["reasoning_effort"]) != string(encoded) {
			body["reasoning_effort"] = encoded
			changed = true
		}
		// Match native WorkBuddy's injectReasoningSummaryIfNeeded. This is a
		// request for visible summaries, not evidence of internal reasoning.
		if _, present := body["reasoning_summary"]; !present {
			summary := requestReasoningSummary(body, req.OriginalRequest)
			if len(summary) == 0 {
				summary = json.RawMessage(`"auto"`)
			}
			body["reasoning_summary"] = summary
			changed = true
		}
	}
	if !changed {
		return payload, nil
	}
	return json.Marshal(body)
}

func requestReasoningSummary(body map[string]json.RawMessage, original []byte) json.RawMessage {
	// Keep an explicit translated/nested summary ahead of the original request.
	var nested map[string]json.RawMessage
	if json.Unmarshal(body["reasoning"], &nested) == nil {
		if raw, ok := nested["summary"]; ok {
			return raw
		}
	}
	var source map[string]json.RawMessage
	if json.Unmarshal(original, &source) != nil {
		return nil
	}
	if raw, ok := source["reasoning_summary"]; ok {
		return raw
	}
	if json.Unmarshal(source["reasoning"], &nested) == nil {
		if raw, ok := nested["summary"]; ok {
			return raw
		}
	}
	return nil
}
