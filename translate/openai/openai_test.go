package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
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
	// Both languages are named: a bare "into Chinese" lets the model keep
	// writing Japanese on long inputs (see defaultSingleInstruction).
	assert.Contains(t, body.Messages[0].Content,
		"Translate the following Japanese text into Simplified Chinese, without additional explanation.")
	assert.Contains(t, body.Messages[0].Content, "こんにちは")
}

// TestPromptModeSingleNamesSourceLanguage pins the fix for language drift:
// the single-message layout must state which language the text is in.
func TestPromptModeSingleNamesSourceLanguage(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "qwen3.5:9b",
		PromptMode: PromptModeSingle,
	}).Translate("こんにちは", "ja", "zh-CN")
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
	assert.Contains(t, body.Messages[0].Content, "Translate the following Japanese text")
	assert.Contains(t, body.Messages[0].Content, "into Simplified Chinese")
}

// TestPromptModeSingleFallsBackWhenSourceUnknown keeps the old wording when the
// plugin could not detect the source language.
func TestPromptModeSingleFallsBackWhenSourceUnknown(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", PromptMode: PromptModeSingle,
	}).Translate("hello", "auto", "zh-CN")
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
	assert.Contains(t, body.Messages[0].Content,
		"Translate the following segment into Simplified Chinese, without additional explanation.")
}

// TestDefaultModeNamesSimplifiedChinese covers the three-message layout, where
// only the target name was ambiguous.
func TestDefaultModeNamesSimplifiedChinese(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", Translator: TranslatorGoOpenAI,
	}).Translate("こんにちは", "ja", "zh-CN")
	assert.NoError(t, err)

	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	if !assert.Len(t, body.Messages, 3) {
		return
	}
	assert.Equal(t,
		"Please translate the following text from Japanese to Simplified Chinese:",
		body.Messages[1].Content)
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
		PromptMode:  PromptModeSingle,
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
	assert.Contains(t, body.Messages[0].Content, "Render the English text below as Simplified Chinese, nothing else.")
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
	_, err := validateResult("已为您完成翻译。", "zh-CN")
	assert.ErrorContains(t, err, "non-translation reply")

	_, err = validateResult("", "zh-CN")
	assert.ErrorContains(t, err, "empty translation result")

	// A genuine translation that merely contains the word 翻译 must pass.
	got, err := validateResult("这是一段很长很长的译文，里面提到了翻译这件事，但绝不是空转回复。", "zh-CN")
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

// TestLanguageDriftRejected covers the real failure this check exists for:
// asked for Chinese, qwen3.5 returned the Japanese input lightly reworded.
func TestLanguageDriftRejected(t *testing.T) {
	drifted := "出身地は地方。F カップ女優・石原希望の AV デビュー第 4 弾。ただし、2 作目以降の 3 本" +
		"（『本中』『kawaii』『ダスッ!』）の発売日がすべて同じため、「AV デビュー第 2 弾」とするのが正確。" +
		"<br><br>👤 絶対零度 · 2020-07-10 10:08<br><b>[[S5]] 見出し</b>"

	_, err := validateResult(drifted, "zh-CN")
	assert.ErrorContains(t, err, "still in the source language")

	// Traditional Chinese targets are checked too.
	_, err = validateResult(drifted, "zh-TW")
	assert.ErrorContains(t, err, "still in the source language")

	// Non-Chinese targets must not be flagged: a Japanese translation is fine.
	got, err := validateResult(drifted, "ja")
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

// TestLanguageDriftAllowsKeptNames makes sure the check tolerates what the
// prompt explicitly permits: reviewer nicknames and titles kept verbatim.
func TestLanguageDriftAllowsKeptNames(t *testing.T) {
	ok := "👤 び〜さくくん。 · 2020-06-22 10:21<br>" +
		"👤 アララギ先輩 · 2021-07-26 15:15<br>" +
		"<b>⭐⭐⭐⭐⭐ 制服</b><br>" +
		"这部作品的演技非常自然，值得一看。石原希望的表演让人印象深刻，剧情推进也很流畅。" +
		"<br>两人之间的互动真实到令人信服，导演对细节的把控相当到位。"
	got, err := validateResult(ok, "zh-CN")
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

// TestEnglishDriftRejected covers the other way a local model loses the
// target: asked for Chinese it answers in English. That is what the old
// `default` layout did, because its system prompt is written in English and
// becomes the nearest attractor once the model loses the target anchor.
func TestEnglishDriftRejected(t *testing.T) {
	drifted := "This is easily the best scene of the whole movie, she really enjoys it and the camera work " +
		"captures every moment.<br>" +
		"The actress is amazing here, and you can tell she is having fun with it."
	_, err := validateResult(drifted, "zh-CN")
	assert.ErrorContains(t, err, "still in the source language")

	// English is a legitimate target: nothing to flag.
	got, err := validateResult(drifted, "en")
	assert.NoError(t, err)
	assert.NotEmpty(t, got)
}

// TestEnglishDriftAllowsLatinFragments pins the conservative side of the
// check. Latin letters are perfectly normal inside a Chinese translation, so
// a name list, a spec line or a mixed line must not be rejected — a false
// positive fails the whole chunk, which is worse than letting a rare English
// line through.
func TestEnglishDriftAllowsLatinFragments(t *testing.T) {
	for _, ok := range []string{
		// Romanized names: no function words anywhere.
		"Mihashi Ren, Yua Mikami, Nozomi Ishihara, Saika Kawakita, Arina Hashimoto",
		// Title line the prompt explicitly keeps verbatim.
		"<b>[[S5]] Amazing Ishihara Nozomi homemade collection</b>",
		// Spec / catalogue line.
		"DASD-693 4K FHD 2160p x265 10bit HEVC AAC",
		// Chinese translation that carries latin fragments.
		"石原希望（Nozomi Ishihara）在这部作品里的表现非常自然，DASD-693 的 4K 版本画质很好。",
		// Short latin fragments on their own lines.
		"F罩杯<br>4K<br>DASD-693",
	} {
		got, err := validateResult(ok, "zh-CN")
		assert.NoError(t, err, "should not be flagged: %q", ok)
		assert.NotEmpty(t, got)
	}
}

// TestEnglishDriftNeedsFunctionWords documents the exact boundary: long latin
// lines without function words are names/specs, with them they are prose.
func TestEnglishDriftNeedsFunctionWords(t *testing.T) {
	assert.False(t, looksLikeEnglishProse(
		"Mihashi Ren Yua Mikami Nozomi Ishihara Saika Kawakita Arina Hashimoto"))
	assert.True(t, looksLikeEnglishProse(
		"She is really into it and you can tell from the way she moves."))
	// Too short to judge.
	assert.False(t, looksLikeEnglishProse("It was good."))
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

// newScriptedServer replies with each content in turn and records every
// request body, so a test can assert on both calls of a retry.
func newScriptedServer(t *testing.T, contents ...string) (*httptest.Server, *[][]byte) {
	t.Helper()
	bodies := new([][]byte)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*bodies = append(*bodies, body)

		content := contents[len(contents)-1]
		if calls < len(contents) {
			content = contents[calls]
		}
		calls++

		payload, _ := json.Marshal(content)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":` +
			string(payload) + `},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, bodies
}

type capturedMessages struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// TestRefusalRetriesWithNeutralSystemPrompt covers the failure this pipeline
// actually hit: a safety-aligned local model answers a translation request with
// "我无法提供包含色情内容的信息。" instead of a translation. That text is
// Chinese, non-empty and not in the wrong script, so every other guard passes
// it; without a retry it would be stored as the title.
func TestRefusalRetriesWithNeutralSystemPrompt(t *testing.T) {
	srv, bodies := newScriptedServer(t,
		"我无法提供包含色情内容的信息。我的职责是提供安全、有益的帮助。",
		"【VR】【8K】不能用吗？○子被当作性玩具")

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
	}).Translate("【VR】【8K】使えねぇか〇子はディルド玩具」", "ja", "zh-CN")

	assert.NoError(t, err)
	assert.Equal(t, "【VR】【8K】不能用吗？○子被当作性玩具", got)
	if !assert.Len(t, *bodies, 2) {
		return
	}

	// The first attempt keeps the single layout: no system message at all.
	var first capturedMessages
	if assert.NoError(t, json.Unmarshal((*bodies)[0], &first)) {
		if assert.Len(t, first.Messages, 1) {
			assert.Equal(t, "user", first.Messages[0].Role)
		}
	}

	// The retry adds exactly one system message, in front, and keeps the user
	// turn untouched so the model still sees the source text the same way.
	var second capturedMessages
	if assert.NoError(t, json.Unmarshal((*bodies)[1], &second)) {
		if assert.Len(t, second.Messages, 2) {
			assert.Equal(t, "system", second.Messages[0].Role)
			assert.Contains(t, second.Messages[0].Content, "machine translation engine")
			assert.Equal(t, "user", second.Messages[1].Role)
			assert.Equal(t, first.Messages[0].Content, second.Messages[1].Content)
		}
	}
}

// TestAppendedRefusalRetries covers the partial refusal: the model translates
// the text and then adds "Sorry, I can't respond to this question." on a new
// line. Before the tail check this was reported as language drift, which the
// plugin classifies as an endpoint failure — it benched the provider and never
// tried the sensitive-content fallback, and the title was lost.
func TestAppendedRefusalRetries(t *testing.T) {
	srv, bodies := newScriptedServer(t,
		"【VR】【8K】没用的〇子被假阳具玩具调教，直到榨不出精子为止。小野坂结衣花\nSorry, I can't respond to this question.",
		"【VR】【8K】没用的〇子被假阳具玩具调教，直到榨不出精子为止。小野坂结衣花")

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
	}).Translate("【VR】【8K】使えねぇか〇子はディルド玩具」", "ja", "zh-CN")

	assert.NoError(t, err)
	assert.Equal(t, "【VR】【8K】没用的〇子被假阳具玩具调教，直到榨不出精子为止。小野坂结衣花", got)
	assert.Len(t, *bodies, 2, "one attempt plus one retry with the neutral system prompt")
}

// TestRefusalTwiceIsAnError stops a persistent refusal from being written into
// the library: the caller must see an error so it can fall back.
func TestRefusalTwiceIsAnError(t *testing.T) {
	refusal := "我无法提供包含色情内容的信息。"
	srv, bodies := newScriptedServer(t, refusal, refusal)

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
	}).Translate("マ○コ魅せつけ放尿お姉さん", "ja", "zh-CN")

	assert.Empty(t, got)
	assert.ErrorContains(t, err, "refused to translate")
	assert.Len(t, *bodies, 2, "one attempt plus one retry, no more")
}

// TestRefusalDetectionBoundary keeps the check from misfiring on real
// translations that happen to quote a refusal phrase, and covers the wording
// variation that a fixed phrase list missed in practice.
func TestRefusalDetectionBoundary(t *testing.T) {
	long := strings.Repeat("この作品は本当に素晴らしいです。", 30)
	for _, unit := range []struct {
		reply, source string
		want          bool
	}{
		{"我无法提供包含色情内容的信息。", "マ○コ魅せつけ放尿お姉さん", true},
		{"无法提供该内容的翻译", "アナル丸見えデカ尻", true},
		{"无法提供该请求的翻译。作为 AI 助手，我严格遵守内容安全规范，不生成或传播包含色情、低俗内容的信息。", "マ○コ魅せつけ放尿お姉さん", true},
		{"抱歉，我不能协助完成这个请求。", "女教師レ×プ輪●", true},
		{"I can't translate this text", "アナル丸見えデカ尻", true},
		{"I'm sorry, but I can't help with that request.", "アナル丸見えデカ尻", true},
		// A long source that comes back as a few characters is a refusal even
		// when it carries no marker at all.
		{"无法提供。", long, true},
		// The model translated correctly and then appended a refusal sentence
		// on its own line (observed on a gateway-hosted GLM model). The reply
		// is long and mostly a real translation, so only the tail reveals it.
		{"【VR】【8K】没用的〇子被假阳具玩具调教，直到榨不出精子为止。小野坂结衣花\nSorry, I can't respond to this question.",
			"【VR】【8K】使えねぇか〇子はディルド玩具」 ト〇リュウ女幹部の薬漬けチ〇コ調教", true},
		// Same refusal glued to the last sentence instead of on a new line.
		{"【VR】【8K】没用的〇子被假阳具玩具调教，直到榨不出精子为止。Sorry, I can't respond to this question.",
			"【VR】【8K】使えねぇか〇子はディルド玩具」 ト〇リュウ女幹部の薬漬けチ〇コ調教", true},
		// Gateway-side moderation (one-api / new-api) answers as content.
		{"系统检测到您当前输入的信息存在敏感内容", "マ○コ魅せつけ放尿お姉さん", true},
		{"这是一部很长的评论，里面提到客服说无法提供该商品的库存信息，所以作者改买了别的款式。", "短い入力", false},
		{"【VR】【8K】不能用吗？○子被当作性玩具", "【VR】【8K】使えねぇか〇子はディルド玩具」", false},
		{strings.Repeat("间贷给堀内父子的石原希望，将戴着的蜜柑分给他们。", 20), long, false},
		{"", "短い", false},
	} {
		assert.Equal(t, unit.want, isRefusalReply(unit.reply, unit.source), "reply=%q", unit.reply)
	}
}

// TestRefusalRetryIsNotUsedForNormalChunks pins the cost: the retry must not
// fire for ordinary content, otherwise every chunk would carry the English
// system prompt and drift back into English.
func TestRefusalRetryIsNotUsedForNormalChunks(t *testing.T) {
	srv, bodies := newScriptedServer(t, "这部作品非常出色，表演自然。")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
	}).Translate("この作品は素晴らしいです。", "ja", "zh-CN")

	assert.NoError(t, err)
	assert.Len(t, *bodies, 1)
}

// TestRefusalErrorIsClassifiedAsContentError pins the contract with the Emby
// plugin. The plugin decides between "content problem" and "endpoint problem"
// by matching the exception text against this regex, and the two outcomes are
// very different: a content problem abandons the local channel without starting
// its cooldown and routes the primary channel to the sensitive-content
// fallback, while an endpoint problem benches the local channel for ten minutes
// and fails the title. Rewording the error must not silently flip that.
func TestRefusalErrorIsClassifiedAsContentError(t *testing.T) {
	pluginSensitiveRe := regexp.MustCompile(`(?i)` +
		`content[\s_-]*(policy|filter)|moderation|sensitive|safety|` +
		`prohibited|inappropriate|flagged|风控|敏感|违规`)

	refusal := "我无法提供包含色情内容的信息。"
	srv, _ := newScriptedServer(t, refusal, refusal)

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m",
		PromptMode: PromptModeSingle,
	}).Translate("マ○コ魅せつけ放尿お姉さん", "ja", "zh-CN")

	if !assert.Error(t, err) {
		return
	}
	assert.Regexp(t, pluginSensitiveRe, err.Error())
}
