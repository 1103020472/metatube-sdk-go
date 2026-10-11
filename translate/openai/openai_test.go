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
			"&openai-disable-thinking=true&openai-max-completion-tokens=2048")

	assert.True(t, target.DisableThinking, "DisableThinking should bind to true")
	assert.Equal(t, 2048, target.MaxCompletionTokens)
}

func TestDefaultMaxCompletionTokens(t *testing.T) {
	assert.Equal(t, 4096, DefaultMaxCompletionTokens)
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

// TestThinkingInjectedIntoRequestBody verifies the on-the-wire body patch.
func TestThinkingInjectedIntoRequestBody(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	got, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "glm-5.3-flash", DisableThinking: true,
	}).Translate("hello", "auto", "zh-CN")
	assert.NoError(t, err)
	assert.Equal(t, "你好", got)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		t.Fatalf("request body is not JSON: %s", *captured)
	}
	thinking, ok := body["thinking"].(map[string]any)
	if !assert.True(t, ok, "thinking missing in body: %s", *captured) {
		return
	}
	assert.Equal(t, "disabled", thinking["type"])
	assert.Equal(t, float64(4096), body["max_completion_tokens"])
	assert.Len(t, body["messages"], 3)
}

// TestThinkingAbsentByDefault keeps the previous behavior untouched.
func TestThinkingAbsentByDefault(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{APIKey: "k", APIUrl: srv.URL, Model: "m"}).Translate("hello", "auto", "en")
	assert.NoError(t, err)

	var body map[string]any
	if !assert.NoError(t, json.Unmarshal(*captured, &body)) {
		return
	}
	_, exists := body["thinking"]
	assert.False(t, exists, "thinking must be absent when DisableThinking=false")
}

// TestCustomMaxCompletionTokensIsForwarded proves the config value reaches the wire.
func TestCustomMaxCompletionTokensIsForwarded(t *testing.T) {
	srv, captured := newMockServer(t, "stop")

	_, err := (&OpenAI{
		APIKey: "k", APIUrl: srv.URL, Model: "m", MaxCompletionTokens: 1234,
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

	_, err := (&OpenAI{APIKey: "k", APIUrl: srv.URL, Model: "m"}).Translate("hello", "auto", "zh-CN")
	assert.Error(t, err, "truncated response must not be reported as success")
}
