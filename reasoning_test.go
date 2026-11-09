package main

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// Independent fixtures from the channel's supplied capability table, not from
// the implementation's profile lookup. A missing Thinking declaration causes
// CPA to strip otherwise-valid named efforts before the executor runs.
var expectedModelSpecs = []struct {
	id, name, levels, defaultEffort string
	input, output                   int64
}{
	{"space-bunny", "Space-Bunny", "low,medium,high,xhigh,max", "max", 1000000, 128000},
	{"minimax-m3.1-flash", "MiniMax-M3.1-Flash", "low,medium,high,xhigh,max", "max", 1000000, 128000},
	{"step-5-preview", "Step-5-Preview", "low,medium,high", "high", 1000000, 64000},
	{"kimi-k3-2", "Kimi-K3", "low,high,xhigh", "high", 1000000, 32000},
	{"glm-5.3", "GLM-5.3", "low,high,max", "high", 1000000, 64000},
	{"glm-5.3-flash", "GLM-5.3-Flash", "low,high,max", "high", 1000000, 131072},
	{"kimi-k2.8-preview", "Kimi-K2.8-Preview", "low,high,max", "high", 1000000, 64000},
	{"kimi-k3.1-pro", "Kimi-K3.1-Pro", "low,high,max", "high", 1000000, 32000},
	{"deepseek-v4.1-flash", "DeepSeek-V4.1-Flash", "low,high,max", "high", 1000000, 128000},
	{"glm-5.2", "GLM-5.2", "high,xhigh", "high", 1000000, 64000},
	{"deepseek-v4-flash", "DeepSeek-V4-Flash", "high,xhigh", "high", 1000000, 50000},
	{"deepseek-v4-pro", "DeepSeek-V4-Pro", "high,xhigh", "high", 1000000, 128000},
	{"hy3", "Hy3", "low,high", "high", 0, 0},
	{"hy4-preview", "Hy4 preview", "high", "high", 960000, 64000},
	{"hy4", "Hy4", "high", "high", 960000, 64000},
	{"mimo-v2.6-pro", "Xiaomi MiMo-V2.6-Pro", "high", "high", 1000000, 128000},
}

func TestDiscoveryAdvertisesChannelReasoningAndHardLimits(t *testing.T) {
	for _, tc := range expectedModelSpecs {
		t.Run(tc.id, func(t *testing.T) {
			// Deliberately stale generic upstream values must not override the channel
			// table. In particular 300K is a selection default, not the hard limit.
			body, _ := json.Marshal(map[string]any{"data": []any{map[string]any{
				"id": tc.id, "name": tc.name, "maxInputTokens": 192000, "maxOutputTokens": 8192,
				"supportsReasoning": true, "onlyReasoning": true,
				"reasoning": map[string]any{"effort": "medium", "supportedEfforts": []string{"medium"}},
			}}})
			client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) { return modelHTTPResponse(200, string(body)), nil })}
			models, err := fetchModels(client, endpointModels, &storedAuth{})
			if err != nil {
				t.Fatal(err)
			}
			if len(models) != 1 {
				t.Fatalf("models=%d", len(models))
			}
			m := models[0]
			if m.Thinking == nil {
				t.Fatal("missing Thinking metadata: CPA would strip reasoning_effort")
			}
			if !reflect.DeepEqual(m.Thinking.Levels, strings.Split(tc.levels, ",")) || m.UserDefined || m.Thinking.ZeroAllowed || m.Thinking.DynamicAllowed {
				t.Fatalf("incorrect capability: %#v, userDefined=%v", m.Thinking, m.UserDefined)
			}
			if tc.input > 0 && (m.InputTokenLimit != tc.input || m.ContextLength != tc.input) {
				t.Errorf("input/context=%d/%d, want %d", m.InputTokenLimit, m.ContextLength, tc.input)
			}
			if tc.output > 0 && (m.OutputTokenLimit != tc.output || m.MaxCompletionTokens != tc.output) {
				t.Errorf("output/completion=%d/%d, want %d", m.OutputTokenLimit, m.MaxCompletionTokens, tc.output)
			}
		})
	}
}

func TestStaticModelsHaveReasoningMetadataWhenKnown(t *testing.T) {
	for _, m := range wbModels() {
		switch m.ID {
		case "glm-5.2", "deepseek-v4-flash", "deepseek-v4-pro":
			if m.Thinking == nil || !reflect.DeepEqual(m.Thinking.Levels, []string{"high", "xhigh"}) || m.UserDefined {
				t.Errorf("static fallback loses capability for %s: %#v", m.ID, m)
			}
		case "hy3":
			if m.Thinking == nil || !reflect.DeepEqual(m.Thinking.Levels, []string{"low", "high"}) {
				t.Error("static Hy3 must support low and high")
			}
		}
	}
}

func TestDiscoveryHandlesNewReasoningModelsWithoutInventingLevels(t *testing.T) {
	client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[
   {"id":"future-declared","supportsReasoning":true,"reasoning":{"supportedEfforts":["low","high","max"]}},
   {"id":"future-opaque","supportsReasoning":true,"onlyReasoning":true},
   {"id":"future-unknown"},
   {"id":"non-reasoning","supportsReasoning":false}
  ]}`), nil
	})}
	models, err := fetchModels(client, endpointModels, &storedAuth{})
	if err != nil {
		t.Fatal(err)
	}
	if models[0].Thinking == nil || !reflect.DeepEqual(models[0].Thinking.Levels, []string{"low", "high", "max"}) {
		t.Error("dropped explicit upstream efforts")
	}
	for _, i := range []int{1, 2} {
		if !models[i].UserDefined || models[i].Thinking != nil {
			t.Errorf("unknown capability must pass through without guessing: %#v", models[i])
		}
	}
	if models[3].UserDefined || models[3].Thinking != nil {
		t.Error("explicit non-reasoning model should remain non-reasoning")
	}
}

const reasoningFixtureSSE = "data: {\"id\":\"test\",\"model\":\"deepseek-v4.1-flash\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"synthetic-reasoning\",\"content\":\"\"}}]}\n\n" +
	"data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"42\",\"reasoning_content\":null},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":5,\"completion_tokens_details\":{\"reasoning_tokens\":3}}}\n\n" +
	"data: [DONE]\n\n"

func executeReasoningFixture(t *testing.T, stream bool, payload map[string]any, original string) (map[string]any, []byte, error) {
	t.Helper()
	before := sharedHTTPClient()
	defer func() { sharedClient = before }()
	var captured map[string]any
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.String() != endpointChat {
			t.Fatalf("unexpected outbound %s %s", r.Method, r.URL)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		return modelHTTPResponse(200, reasoningFixtureSSE), nil
	})}
	data, _ := json.Marshal(payload)
	storage, _ := json.Marshal(storedAuth{Auth: storedTokens{AccessToken: "offline-test-only"}})
	model, _ := payload["model"].(string)
	req := pluginapi.ExecutorRequest{Model: model, Payload: data, StorageJSON: storage, OriginalRequest: []byte(original)}
	raw, _ := json.Marshal(req)
	var result []byte
	var err error
	if stream {
		result, err = handleExecStream(raw)
	} else {
		result, err = handleExecExecute(raw)
	}
	return captured, result, err
}

func TestExecutorForwardsEverySupportedEffortAndDefaults(t *testing.T) {
	for _, tc := range expectedModelSpecs {
		for _, effort := range append(strings.Split(tc.levels, ","), "") {
			for _, stream := range []bool{false, true} {
				label := effort
				if label == "" {
					label = "default"
				}
				mode := "execute"
				if stream {
					mode = "stream"
				}
				t.Run(tc.id+"/"+label+"/"+mode, func(t *testing.T) {
					payload := map[string]any{"model": tc.id, "messages": []any{}, "stream": true}
					if effort != "" {
						payload["reasoning_effort"] = effort
					}
					captured, _, err := executeReasoningFixture(t, stream, payload, "")
					if err != nil {
						t.Fatal(err)
					}
					want := effort
					if want == "" {
						want = tc.defaultEffort
					}
					if captured["reasoning_effort"] != want {
						t.Errorf("outgoing effort=%v, want %s", captured["reasoning_effort"], want)
					}
					if captured["reasoning_summary"] != "auto" {
						t.Errorf("native-client-compatible summary absent: %v", captured["reasoning_summary"])
					}
					if _, ok := captured["thinking"]; ok {
						t.Error("must not invent direct-DeepSeek thinking.type for CodeBuddy")
					}
				})
			}
		}
	}
}

func TestExecutorRejectsUnsupportedEffortsBeforeHTTP(t *testing.T) {
	for _, effort := range []any{"medium", "xhigh", "ultra", "none", "auto", 123, map[string]any{"effort": "high"}} {
		for _, stream := range []bool{false, true} {
			captured, _, err := executeReasoningFixture(t, stream, map[string]any{"model": "deepseek-v4.1-flash", "messages": []any{}, "reasoning_effort": effort}, "")
			if err == nil || captured != nil {
				t.Errorf("unsupported effort %#v reached HTTP (stream=%v): captured=%v, err=%v", effort, stream, captured, err)
			}
		}
	}
}

func TestExecutorPreservesExplicitSummaryChoice(t *testing.T) {
	for _, tc := range []struct {
		field          any
		original, want string
	}{
		{"none", "", "none"}, {"detailed", "", "detailed"},
		{nil, `{"reasoning":{"effort":"high","summary":"none"}}`, "none"},
		{nil, `{"reasoning":{"effort":"high","summary":"detailed"}}`, "detailed"},
	} {
		payload := map[string]any{"model": "deepseek-v4.1-flash", "reasoning_effort": "high", "messages": []any{}}
		if tc.field != nil {
			payload["reasoning_summary"] = tc.field
		}
		captured, _, err := executeReasoningFixture(t, true, payload, tc.original)
		if err != nil {
			t.Fatal(err)
		}
		if captured["reasoning_summary"] != tc.want {
			t.Errorf("summary=%v, want %s", captured["reasoning_summary"], tc.want)
		}
	}
}

func TestSystemTemplateRewriteDoesNotOverrideHy3Low(t *testing.T) {
	raw := rewriteSystemForUpstream([]byte(`{"model":"hy3","reasoning_effort":"low","messages":[{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude."}]}`))
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["reasoning_effort"] != "low" {
		t.Fatal("system sanitization must not overwrite reasoning choice")
	}
	if !strings.Contains(string(raw), "CLI tool") {
		t.Error("lost existing template compatibility")
	}
}

func TestExecutorPreservesReasoningAndUsageInBothModes(t *testing.T) {
	for _, stream := range []bool{false, true} {
		_, raw, err := executeReasoningFixture(t, stream, map[string]any{"model": "deepseek-v4.1-flash", "messages": []any{}, "stream": true, "reasoning_effort": "high"}, "")
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		var messages []map[string]any
		if stream {
			var res streamResponse
			if err := json.Unmarshal(env.Result, &res); err != nil {
				t.Fatal(err)
			}
			for _, chunk := range res.Chunks {
				var m map[string]any
				if err := json.Unmarshal([]byte(stripDataPrefix(string(chunk.Payload))), &m); err != nil {
					t.Fatal(err)
				}
				messages = append(messages, m)
			}
		} else {
			var res pluginapi.ExecutorResponse
			if err := json.Unmarshal(env.Result, &res); err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(res.Payload, &m); err != nil {
				t.Fatal(err)
			}
			messages = append(messages, m)
		}
		reason := ""
		tokens := float64(0)
		for _, m := range messages {
			for _, c := range m["choices"].([]any) {
				choice := c.(map[string]any)
				key := "message"
				if stream {
					key = "delta"
				}
				if msg, ok := choice[key].(map[string]any); ok {
					if s, ok := msg["reasoning_content"].(string); ok {
						reason += s
					}
				}
			}
			if u, ok := m["usage"].(map[string]any); ok {
				tokens = u["completion_tokens_details"].(map[string]any)["reasoning_tokens"].(float64)
			}
		}
		if reason != "synthetic-reasoning" || tokens != 3 {
			t.Errorf("stream=%v reasoning=%q tokens=%v", stream, reason, tokens)
		}
	}
}

// The host can supply equivalent provider credentials with different JSON key
// order/host-only fields at discovery and execution. A raw-byte cache key loses
// the discovered display-name policy and therefore the model's default effort.
func TestDiscoveredDisplayNameDefaultSurvivesCredentialReserialization(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[{"id":"opaque-channel-test-id","name":"DeepSeek-V4.1-Flash"}]}`), nil
	})}
	storage := []byte(`{"type":"workbuddy","auth":{"accessToken":"offline-test-only"},"account":{}}`)
	request, _ := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	models := modelsFromRPC(t, request)
	if len(models) != 1 || models[0].Thinking == nil {
		t.Fatal("profile discovery failed")
	}
	captured, _, err := executeReasoningFixture(t, true, map[string]any{"model": "opaque-channel-test-id", "messages": []any{}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if captured["reasoning_effort"] != "high" {
		t.Fatalf("lost default after host reserialized the same credential: %v", captured)
	}
}

func TestDiscoveredOptionalThinkingCanBeDisabled(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[{"id":"future-toggle-model","supportsReasoning":true,"reasoning":{"supportedEfforts":["low","high"],"canDisableThinking":true}}]}`), nil
	})}
	storage, _ := json.Marshal(storedAuth{Auth: storedTokens{AccessToken: "offline-test-only"}})
	request, _ := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	models := modelsFromRPC(t, request)
	if len(models) != 1 || models[0].Thinking == nil || !models[0].Thinking.ZeroAllowed {
		t.Fatal("explicit disable capability lost")
	}
	captured, _, err := executeReasoningFixture(t, true, map[string]any{"model": "future-toggle-model", "messages": []any{}, "reasoning_effort": "none", "reasoning_summary": "none"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if captured["reasoning_effort"] != "none" {
		t.Fatalf("explicit upstream disable capability was not honored: %v", captured)
	}
}

func TestLegacyHy3PreviewPreservesDefaultAndExplicitChoices(t *testing.T) {
	for _, model := range []string{"hy3-preview", "hy3-preview-agent"} {
		for _, effort := range []string{"", "low", "high"} {
			for _, stream := range []bool{false, true} {
				payload := map[string]any{"model": model, "messages": []any{}}
				if effort != "" {
					payload["reasoning_effort"] = effort
				}
				captured, _, err := executeReasoningFixture(t, stream, payload, "")
				if err != nil {
					t.Fatal(err)
				}
				want := effort
				if want == "" {
					want = "high"
				}
				if captured["reasoning_effort"] != want {
					t.Errorf("model=%s stream=%v effort=%q got=%v want=%s", model, stream, effort, captured["reasoning_effort"], want)
				}
			}
		}
	}
}

func TestLegacyDefaultOnlyProfileKeepsDeclaredUpstreamConstraints(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[{"id":"hy3-preview","supportsReasoning":true,"reasoning":{"supportedEfforts":["high"]}}]}`), nil
	})}
	storage, _ := json.Marshal(storedAuth{Auth: storedTokens{AccessToken: "offline-test-only"}})
	request, _ := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	models := modelsFromRPC(t, request)
	defer discoveredModels.Delete(modelCacheKey(storage))
	if len(models) != 1 || models[0].UserDefined || models[0].Thinking == nil || !reflect.DeepEqual(models[0].Thinking.Levels, []string{"high"}) {
		t.Fatalf("default-only profile discarded explicit upstream capability: %#v", models)
	}
	captured, _, err := executeReasoningFixture(t, true, map[string]any{"model": "hy3-preview", "messages": []any{}, "reasoning_effort": "low"}, "")
	if err == nil || captured != nil {
		t.Fatalf("upstream-declared high-only model accepted low: captured=%v err=%v", captured, err)
	}
	captured, _, err = executeReasoningFixture(t, true, map[string]any{"model": "hy3-preview", "messages": []any{}}, "")
	if err != nil || captured["reasoning_effort"] != "high" {
		t.Fatalf("lost legacy default: captured=%v err=%v", captured, err)
	}
}

func TestLegacyDefaultDoesNotOverrideExplicitNonReasoningCapability(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[{"id":"hy3-preview","supportsReasoning":false},{"id":"hy3-preview-agent","supportsReasoning":false}]}`), nil
	})}
	storage, _ := json.Marshal(storedAuth{Auth: storedTokens{AccessToken: "offline-test-only"}})
	request, _ := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	models := modelsFromRPC(t, request)
	defer discoveredModels.Delete(modelCacheKey(storage))
	for _, m := range models {
		if m.UserDefined || m.Thinking != nil {
			t.Errorf("negative capability became unknown: %#v", m)
		}
		if hasEffort(m.SupportedParameters, "reasoning_effort") {
			t.Errorf("non-reasoning model advertises reasoning_effort: %s", m.ID)
		}
		captured, _, err := executeReasoningFixture(t, true, map[string]any{"model": m.ID, "messages": []any{}}, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := captured["reasoning_effort"]; ok {
			t.Errorf("legacy default overrode explicit negative capability: %v", captured)
		}
		captured, _, err = executeReasoningFixture(t, true, map[string]any{"model": m.ID, "messages": []any{}, "reasoning_effort": "high"}, "")
		if err == nil || captured != nil {
			t.Errorf("explicit non-reasoning capability accepted effort: captured=%v err=%v", captured, err)
		}
	}
}
