package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type modelRoundTripFunc func(*http.Request) (*http.Response, error)

func (f modelRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func modelHTTPResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
}

func TestModelsForAuthDiscoversFullCatalogue(t *testing.T) {
	// Exercise the actual RPC entry point: it used to ignore StorageJSON and
	// return the same ten hard-coded models for every credential.
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	calls := 0
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != endpointModels {
			t.Fatalf("unexpected discovery request: %s %s", r.Method, r.URL)
		}
		for key, want := range map[string]string{
			"Authorization": "Bearer test-access-token", "X-User-Id": "test-user",
			"X-Enterprise-Id": "test-enterprise", "X-Domain": "test-domain",
		} {
			if got := r.Header.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		items := make([]map[string]any, 0, 25)
		for i := 0; i < 25; i++ {
			items = append(items, map[string]any{"id": "new-model-" + string(rune('a'+i))})
		}
		body, _ := json.Marshal(map[string]any{"object": "list", "data": items})
		return modelHTTPResponse(200, string(body)), nil
	})}
	storage, _ := json.Marshal(storedAuth{
		Auth:    storedTokens{AccessToken: "test-access-token", Domain: "test-domain"},
		Account: storedAccount{UID: "test-user", EnterpriseID: "test-enterprise"},
	})
	request, _ := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	raw, err := handleMethod(pluginabi.MethodModelForAuth, request)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		OK     bool                    `json:"ok"`
		Result pluginapi.ModelResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Result.Provider != providerName || len(result.Result.Models) != 25 || calls != 1 {
		t.Fatalf("incomplete discovery: %#v, calls=%d", result, calls)
	}
	if _, err := handleMethod(pluginabi.MethodModelStatic, nil); err != nil || calls != 1 {
		t.Fatalf("static registration should work without discovery: %v, calls=%d", err, calls)
	}
}

func TestFetchModelsPaginationAndMetadata(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Query().Get("platform") != "CLI" {
			t.Fatal("pagination discarded the base URL query")
		}
		switch r.URL.Query().Get("after") {
		case "":
			return modelHTTPResponse(200, `{"data":[{"id":"glm-5.2"},{"id":"new/model","name":"New Model","context_length":123456,"max_completion_tokens":16000,"owned_by":"upstream","created":123}],"has_more":true,"last_id":"new/model"}`), nil
		case "new/model":
			return modelHTTPResponse(200, `{"data":[{"id":"new/model"},{"id":""},{"id":"last-model"}]}`), nil
		default:
			t.Fatalf("unexpected cursor %q", r.URL.Query().Get("after"))
			return nil, nil
		}
	})}
	models, err := fetchModels(client, "https://models.example/v1/models?platform=CLI", &storedAuth{})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, len(models))
	for i, m := range models {
		ids[i] = m.ID
	}
	if !reflect.DeepEqual(ids, []string{"glm-5.2", "new/model", "last-model"}) || calls != 2 {
		t.Fatalf("models=%v, calls=%d", ids, calls)
	}
	if models[0].ContextLength != 1000000 {
		t.Fatal("lost known model metadata")
	}
	m := models[1]
	if m.Name != "new/model" || m.DisplayName != "New Model" || m.ContextLength != 123456 || m.MaxCompletionTokens != 16000 || m.OwnedBy != "upstream" || m.Created != 123 || !m.UserDefined {
		t.Fatalf("lost upstream metadata: %#v", m)
	}
}

func TestReadModelPage(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		wantError  bool
	}{
		{"openai", `{"data":[{"id":"new-model"}]}`, 200, false},
		{"empty catalogue", `{"data":[]}`, 200, false},
		{"wrapped", `{"code":0,"data":{"data":[{"id":"new-model"}]}}`, 200, false},
		{"wrapped array", `{"code":0,"data":[{"id":"new-model"}]}`, 200, false},
		{"unauthorized", `{"error":"expired token"}`, 401, true},
		{"application error", `{"code":11217,"msg":"failure","data":[]}`, 200, true},
		{"missing list", `{}`, 200, true},
		{"null list", `{"data":null}`, 200, true},
		{"wrong list type", `{"data":{}}`, 200, true},
		{"invalid json", `<html>error</html>`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readModelPage(modelHTTPResponse(tc.status, tc.body))
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestFetchModelsRejectsStuckPaginationAndPartialResults(t *testing.T) {
	for _, body := range []string{
		`{"data":[],"has_more":true}`,
		`{"data":[{"id":"same"}],"has_more":true}`,
	} {
		calls := 0
		client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls > 2 {
				t.Fatal("pagination did not stop")
			}
			return modelHTTPResponse(200, body), nil
		})}
		if models, err := fetchModels(client, endpointModels, &storedAuth{}); err == nil || models != nil {
			t.Fatalf("returned a partial catalogue: models=%v, err=%v", models, err)
		}
	}
	client := &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Query().Get("after") != "" {
			return nil, errors.New("connection lost")
		}
		return modelHTTPResponse(200, `{"data":[{"id":"first"}],"has_more":true}`), nil
	})}
	if models, err := fetchModels(client, endpointModels, &storedAuth{}); err == nil || models != nil {
		t.Fatalf("returned partial results after failed page: models=%v, err=%v", models, err)
	}
}

func TestModelsForAuthRequiresCredentials(t *testing.T) {
	for _, raw := range []string{`{`, `{"StorageJSON":"invalid-base64"}`} {
		if _, err := handleModelsForAuth([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid credentials: %s", raw)
		}
	}
}

// A discovery error is fatal to account model registration in CPA. These tests
// cover the host-facing response, not just the upstream parser.
func TestModelsForAuthFallsBackOnDiscoveryFailure(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unsupported endpoint", 404, `{"error":"not found"}`},
		{"expired token", 401, `{"error":"unauthorized"}`},
		{"server failure", 500, `{"error":"unavailable"}`},
		{"bad response", 200, `<html>error</html>`},
		{"empty list", 200, `{"data":[]}`},
		{"no usable ids", 200, `{"data":[{"id":""}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				return modelHTTPResponse(tc.status, tc.body), nil
			})}
			request := modelAuthRequest(t, "fallback-"+tc.name)
			models := modelsFromRPC(t, request)
			if !reflect.DeepEqual(models, wbModels()) {
				t.Fatalf("lost working models: %#v", models)
			}
		})
	}
	for _, raw := range []string{`{}`, `{"StorageJSON":"e30="}`} {
		if models := modelsFromRPC(t, []byte(raw)); !reflect.DeepEqual(models, wbModels()) {
			t.Fatal("lost static models when storage was not available")
		}
	}
}

func TestModelsForAuthKeepsLastSuccessfulCatalogue(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	request := modelAuthRequest(t, "cached-credential")
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return modelHTTPResponse(200, `{"data":[{"id":"discovered-model"}]}`), nil
	})}
	want := modelsFromRPC(t, request)
	if len(want) != 1 || want[0].ID != "discovered-model" {
		t.Fatal("discovery failed")
	}
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("network unavailable")
	})}
	if got := modelsFromRPC(t, request); !reflect.DeepEqual(got, want) {
		t.Fatalf("discarded last working catalogue: %#v", got)
	}
	if got := modelsFromRPC(t, modelAuthRequest(t, "other-credential")); !reflect.DeepEqual(got, wbModels()) {
		t.Fatal("reused model catalogue from another credential")
	}
}

func TestModelsForAuthTimeoutStillRegistersModels(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("missing discovery deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	if got := modelsFromRPC(t, modelAuthRequest(t, "timed-out-credential")); !reflect.DeepEqual(got, wbModels()) {
		t.Fatal("timeout prevented registration of working models")
	}
}

func modelAuthRequest(t *testing.T, token string) []byte {
	t.Helper()
	storage, err := json.Marshal(storedAuth{Auth: storedTokens{AccessToken: token}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(pluginapi.AuthModelRequest{StorageJSON: storage})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func modelsFromRPC(t *testing.T, request []byte) []pluginapi.ModelInfo {
	t.Helper()
	raw, err := handleMethod(pluginabi.MethodModelForAuth, request)
	if err != nil {
		t.Fatalf("discovery interrupted account registration: %v", err)
	}
	var result struct {
		OK     bool                    `json:"ok"`
		Result pluginapi.ModelResponse `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if !result.OK || result.Result.Provider != providerName {
		t.Fatalf("invalid registration response: %s", raw)
	}
	return result.Result.Models
}

func TestModelsForAuthUsesCodeBuddyConfig(t *testing.T) {
	original := sharedHTTPClient()
	defer func() { sharedClient = original }()
	sharedClient = &http.Client{Transport: modelRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v3/config" {
			t.Fatalf("wrong discovery endpoint: %s", r.URL)
		}
		return modelHTTPResponse(200, `{"code":0,"data":{"models":[
			{"id":"glm-5.3","name":"GLM-5.3","maxInputTokens":1000000,"maxOutputTokens":64000,"supportsImages":true},
			{"id":"hy4-preview","maxInputTokens":960000,"maxOutputTokens":64000},
			{"id":"image-only","tags":["text-to-image"]},
			{"id":"video-only","tags":["text-to-video","image-to-video"]},
			{"id":"disabled","disabled":true}
		]}}`), nil
	})}
	models := modelsFromRPC(t, modelAuthRequest(t, "config-credential"))
	if len(models) != 2 || models[0].ID != "glm-5.3" || models[1].ID != "hy4-preview" {
		t.Fatalf("incorrect chat catalogue: %#v", models)
	}
	m := models[0]
	if m.InputTokenLimit != 1000000 || m.OutputTokenLimit != 64000 || m.ContextLength != 1000000 || m.MaxCompletionTokens != 64000 || m.DisplayName != "GLM-5.3" || !reflect.DeepEqual(m.SupportedInputModalities, []string{"text", "image"}) {
		t.Fatalf("lost native CodeBuddy metadata: %#v", m)
	}
}
