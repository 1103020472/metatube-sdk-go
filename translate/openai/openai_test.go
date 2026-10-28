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

// TestPromptModeSingleOmitsSystemMessage pins the hy-mt2 layout: exactly one
// user message, no system message, instruction merged with the source text.
func TestPromptModeSingleOmitsSystemMessage(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "kaelri/hy-mt2:7b",
		PromptMode: PromptModeSingle,
	}).Translate("こんにちは", "ja", "zh-CN")
	assert.NoError(t, err)

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		t.Fatalf("request body is not JSON: %s", *captured)
	}
	if !assert.Len(t, body.Messages, 1) {
		return
	}
	assert.Equal(t, "user", body.Messages[0].Role)
	assert.Contains(t, body.Messages[0].Content,
		"Translate the following segment into Chinese, without additional explanation.")
	assert.Contains(t, body.Messages[0].Content, "こんにちは")
}

// TestPromptModeSystemSingleKeepsSystem proves the middle ground keeps the
// system prompt but still merges instruction and text.
func TestPromptModeSystemSingleKeepsSystem(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSystemSingle, Prompt: "custom system",
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	if !assert.Len(t, body.Messages, 2) {
		return
	}
	assert.Equal(t, "system", body.Messages[0].Role)
	assert.Equal(t, "custom system", body.Messages[0].Content)
	assert.Equal(t, "user", body.Messages[1].Role)
}

// TestPromptModeUpgradesToGoOpenAI keeps a non-default layout working even when
// the caller left the translator at its legacy default.
func TestPromptModeUpgradesToGoOpenAI(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", PromptMode: PromptModeSingle,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Len(t, body["messages"], 1, "single mode must not fall back to the legacy 3-message layout")
	assert.Equal(t, float64(4096), body["max_completion_tokens"],
		"legacy path would have forced 1000")
}

// TestUnknownPromptModeRejected mirrors the translator-name validation.
func TestUnknownPromptModeRejected(t *testing.T) {
	_, err := (&OpenAI{
		APIKey: "k", APIUrl: "http://127.0.0.1:1", Model: "m", PromptMode: "merged",
	}).Translate("hello", "auto", "en")
	assert.ErrorContains(t, err, "unknown prompt mode")
}

// TestReasoningEffortIsForwarded covers the Qwen3-family requirement: without
// reasoning_effort=none the whole budget is spent on hidden reasoning.
func TestReasoningEffortIsForwarded(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "qwen3.5:9b",
		Translator: TranslatorGoOpenAI, ReasoningEffort: "none",
	}).Translate("hello", "auto", "zh-CN")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Equal(t, "none", body["reasoning_effort"])
}

// TestReasoningEffortAbsentByDefault keeps non-reasoning providers untouched.
func TestReasoningEffortAbsentByDefault(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Translator: TranslatorGoOpenAI,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	_, exists := body["reasoning_effort"]
	assert.False(t, exists)
}

// TestCustomInstructionPlaceholders verifies {target}/{source} substitution.
func TestCustomInstructionPlaceholders(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
		Instruction: "Render the {source} text below as {target}, nothing else.",
	}).Translate("hello", "en", "zh-CN")
	assert.NoError(t, err)

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	if !assert.Len(t, body.Messages, 1) {
		return
	}
	assert.Contains(t, body.Messages[0].Content, "Render the English text below as Chinese, nothing else.")
}

// TestControlTokensStripped covers the mangled stop list in the hy-mt2 GGUF:
// the terminator reaches the client and must never surface as a translation.
func TestControlTokensStripped(t *testing.T) {
	for _, unit := range []struct{ in, want string }{
		{"译文内容<|hy-Assistant|>", "译文内容"},
		{"译文内容<｜hy_end▁of▁sentence｜>", "译文内容"},
		{"译文内容<\ufffd|hy-Assistant\ufffd|>", "译文内容"},
		{"译文内容<|endoftext|>尾巴", "译文内容尾巴"},
		{"  正常译文  ", "正常译文"},
	} {
		assert.Equal(t, unit.want, stripControlTokens(unit.in), "input %q", unit.in)
	}
}

// TestCannedReplyRejected stops translation-only models from poisoning the
// library with "已为您完成翻译。" when the input is not a sentence.
func TestCannedReplyRejected(t *testing.T) {
	_, err := validateResult("已为您完成翻译。")
	assert.ErrorContains(t, err, "non-translation reply")

	_, err = validateResult("")
	assert.ErrorContains(t, err, "empty translation result")

	// A genuine translation that merely contains the word 翻译 must pass.
	got, err := validateResult("这是一段很长很长的译文，里面提到了翻译这件事，但绝不是空转回复。")
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

func floatPtr(f float64) *float64 { return &f }

// TestTemperatureZeroReachesWire is the whole point of the override path:
// go-openai tags Temperature with omitempty, so 0 would otherwise vanish and
// the model would keep sampling randomly.
func TestTemperatureZeroReachesWire(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "qwen3.5:9b",
		Temperature: floatPtr(0),
	}).Translate("hello", "auto", "zh-CN")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		t.Fatalf("request body is not JSON: %s", *captured)
	}
	assert.Equal(t, float64(0), body["temperature"],
		"explicit temperature 0 must survive omitempty")
}

// TestTemperatureOverridesExtraParams pins the precedence: the dedicated field
// wins over an ExtraParams entry with the same key.
func TestTemperatureOverridesExtraParams(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		Translator:  TranslatorGoOpenAI,
		ExtraParams: `{"temperature": 0.9, "enable_thinking": false}`,
		Temperature: floatPtr(0),
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Equal(t, float64(0), body["temperature"])
	assert.Equal(t, false, body["enable_thinking"])
}

// TestTemperatureAbsentByDefault leaves non-configured providers untouched.
func TestTemperatureAbsentByDefault(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Translator: TranslatorGoOpenAI,
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	_, exists := body["temperature"]
	assert.False(t, exists, "temperature must be absent when not configured")
}

// TestTemperatureUpgradesToGoOpenAI keeps the legacy library's hard-coded 0.1
// from silently overriding the caller's choice.
func TestTemperatureUpgradesToGoOpenAI(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Temperature: floatPtr(0),
	}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	assert.Equal(t, float64(0), body["temperature"])
	assert.Equal(t, float64(4096), body["max_completion_tokens"],
		"legacy path would have forced 1000")
}

// TestTemperatureBindsFromRouteQuery proves the plugin can pass it as a query
// parameter, including the 0 that schema decoding must not treat as empty.
func TestTemperatureBindsFromRouteQuery(t *testing.T) {
	target := decodeViaRoute(t,
		"q=hello&from=auto&to=zh-CN&engine=OpenAi&openai-temperature=0")

	if !assert.NotNil(t, target.Temperature) {
		return
	}
	assert.Equal(t, float64(0), *target.Temperature)
}
