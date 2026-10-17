package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gorilla/schema"
	openai "github.com/sashabaranov/go-openai"
	"github.com/stretchr/testify/assert"

	"github.com/1103020472/metatube-sdk-go/translate"
)

func TestOpenaiTranslate(t *testing.T) {
	if os.Getenv("OPENAI_API_KEY") == "" {
		t.Skip("OPENAI_API_KEY not set; skipping live integration test")
	}
	for _, unit := range []struct {
		text, from, to string
	}{
		{`Oh yeah! I'm a translator!`, "", "zh-CN"},
		{`Oh yeah! I'm a translator!`, "", "zh-TW"},
		{`Oh yeah! I'm a translator!`, "", "ja"},
		{`Oh yeah! I'm a translator!`, "", "de"},
		{`Oh yeah! I'm a translator!`, "", "fr"},
	} {
		result, err := (&OpenAI{
			APIKey: os.Getenv("OPENAI_API_KEY"),
			APIUrl: os.Getenv("OPENAI_API_URL"),
			Model:  openai.GPT4o,
		}).Translate(unit.text, unit.from, unit.to)
		if assert.NoError(t, err) {
			t.Log(result)
		}
	}
}

// decodeViaRoute mimics route/translate.go binding, proving that the plugin's
// query parameter names actually populate the OpenAI struct fields.
func decodeViaRoute(t *testing.T, query string) *OpenAI {
	t.Helper()
	decoder := schema.NewDecoder()
	decoder.SetAliasTag("json")
	decoder.IgnoreUnknownKeys(true)

	req := httptest.NewRequest(http.MethodGet, "/v1/translate?"+query, nil)
	target := &OpenAI{}
	if err := decoder.Decode(target, req.URL.Query()); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	return target
}

func TestRouteQueryBindsNewOptions(t *testing.T) {
	target := decodeViaRoute(t,
		"q=hello&from=auto&to=zh-CN&engine=OpenAi"+
			"&openai-api-key=k&openai-api-url=u&openai-model=m"+
			"&openai-translator=go-openai"+
			"&openai-extra-params=%7B%22enable_thinking%22%3Afalse%7D"+
			"&openai-max-completion-tokens=2048")

	assert.Equal(t, TranslatorGoOpenAI, target.Translator)
	assert.JSONEq(t, `{"enable_thinking":false}`, target.ExtraParams)
	assert.Equal(t, 2048, target.MaxCompletionTokens)
}

func TestDefaultMaxCompletionTokens(t *testing.T) {
	assert.Equal(t, 4096, DefaultMaxCompletionTokens)
}

// TestFactoryResolvesOpenAiEngine exercises the real route path: the plugin
// sends engine=OpenAi, and translate.New must resolve it to *OpenAI while
// binding the new query parameters.
func TestFactoryResolvesOpenAiEngine(t *testing.T) {
	decoder := schema.NewDecoder()
	decoder.SetAliasTag("json")
	decoder.IgnoreUnknownKeys(true)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/translate?engine=OpenAi&openai-translator=go-openai", nil)
	got := translate.New("OpenAi", func(v any) error {
		return decoder.Decode(v, req.URL.Query())
	})

	oa, ok := got.(*OpenAI)
	if !assert.True(t, ok, "engine OpenAi must resolve to *OpenAI, got %T", got) {
		return
	}
	assert.Equal(t, TranslatorGoOpenAI, oa.Translator)
}

func TestResolveTranslator(t *testing.T) {
	for _, unit := range []struct {
		in, want string
	}{
		{"", TranslatorOpenAITranslator},
		{"openai-translator", TranslatorOpenAITranslator},
		{"OpenAI-Translator", TranslatorOpenAITranslator},
		{"  openai_translator ", TranslatorOpenAITranslator},
		{"go-openai", TranslatorGoOpenAI},
		{"Go_OpenAI", TranslatorGoOpenAI},
		{"GO OPENAI", TranslatorGoOpenAI},
	} {
		got, err := resolveTranslator(unit.in)
		if assert.NoError(t, err, "input %q", unit.in) {
			assert.Equal(t, unit.want, got, "input %q", unit.in)
		}
	}
}

func TestUnknownTranslatorRejected(t *testing.T) {
	// Must fail before any network call: the typo would otherwise silently
	// disable ExtraParams/MaxCompletionTokens.
	_, err := (&OpenAI{
		APIKey: "k", APIUrl: "http://127.0.0.1:1", Model: "m", Translator: "goopenai2",
	}).Translate("hello", "auto", "zh-CN")
	assert.ErrorContains(t, err, "unknown translator")
}

// newMockServer captures the outgoing request body and returns a canned reply.
func newMockServer(t *testing.T, finishReason string) (*httptest.Server, *[]byte) {
	t.Helper()
	captured := new([]byte)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"你好"},` +
			`"finish_reason":"` + finishReason + `"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, captured
}

// TestDefaultTranslatorUsesOpenAITranslator pins the default to the legacy
// xjasonlyu/openai-translator implementation. Its DefaultOptions forces
// max_completion_tokens=1000 and temperature=0.1, and it cannot forward the
// plugin's ExtraParams / MaxCompletionTokens.
func TestDefaultTranslatorUsesOpenAITranslator(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		ExtraParams:         `{"enable_thinking": false}`,
		MaxCompletionTokens: 2048,
	}).Translate("hello", "auto", "zh-CN")
	assert.NoError(t, err)
	assert.Equal(t, "你好", got)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		t.Fatalf("request body is not JSON: %s", *captured)
	}
	assert.Equal(t, float64(1000), body["max_completion_tokens"],
		"default path must keep the upstream library's default bound")
	assert.InDelta(t, 0.1, body["temperature"], 1e-6)
	_, exists := body["enable_thinking"]
	assert.False(t, exists, "extra params must not leak into the openai-translator path")
}

// TestExtraParamsInjectedIntoRequestBody verifies the on-the-wire body patch.
func TestExtraParamsInjectedIntoRequestBody(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "glm-5.3-flash",
		Translator:  TranslatorGoOpenAI,
		ExtraParams: `{"enable_thinking": false}`,
	}).Translate("hello", "auto", "zh-CN")
	assert.NoError(t, err)
	assert.Equal(t, "你好", got)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		t.Fatalf("request body is not JSON: %s", *captured)
	}
	assert.Equal(t, false, body["enable_thinking"])
	assert.Equal(t, float64(4096), body["max_completion_tokens"])
	assert.Len(t, body["messages"], 3)
}

// TestNestedExtraParamsInjected covers the Zhipu-style nested object.
func TestNestedExtraParamsInjected(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		Translator:  TranslatorGoOpenAI,
		ExtraParams: `{"thinking": {"type": "disabled"}}`,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !assert.True(t, ok, "thinking missing: %s", *captured) {
		return
	}
	assert.Equal(t, "disabled", thinking["type"])
}

// TestExtraParamsAbsentByDefault keeps the go-openai behavior untouched.
func TestExtraParamsAbsentByDefault(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Translator: TranslatorGoOpenAI,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	_, exists := body["enable_thinking"]
	assert.False(t, exists, "enable_thinking must be absent when ExtraParams is empty")
}

// TestInvalidExtraParamsRejected fails loudly instead of silently ignoring config.
func TestInvalidExtraParamsRejected(t *testing.T) {
	srv, _ := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		Translator: TranslatorGoOpenAI, ExtraParams: `{not json`,
	}).Translate("hello", "auto", "en")
	assert.Error(t, err, "malformed extra params must surface as an error")
}

// TestExtraParamsDoNotOverwriteExplicitFields guards the merge precedence.
func TestExtraParamsDoNotOverwriteExplicitFields(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "the-real-model",
		Translator:  TranslatorGoOpenAI,
		ExtraParams: `{"model": "hijacked", "enable_thinking": false}`,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Equal(t, "the-real-model", body["model"], "extra params must not override the real model")
	assert.Equal(t, false, body["enable_thinking"])
}

// TestCustomMaxCompletionTokensIsForwarded proves the config value reaches the wire.
func TestCustomMaxCompletionTokensIsForwarded(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		Translator: TranslatorGoOpenAI, MaxCompletionTokens: 1234,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Equal(t, float64(1234), body["max_completion_tokens"])
}

// TestTruncationIsReported guards against silently returning partial text.
func TestTruncationIsReported(t *testing.T) {
	srv, _ := newMockServer(t, "length")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Translator: TranslatorGoOpenAI,
	}).Translate("hello", "auto", "zh-CN")
	assert.Error(t, err, "truncated response must not be reported as success")
	assert.ErrorContains(t, err, "truncated by max_completion_tokens")
}
