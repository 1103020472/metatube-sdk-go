package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/1103020472/metatube-sdk-go/translate"
)

var _ translate.Translator = (*OpenAI)(nil)

const defaultSystemPrompt = `You are a professional translator for adult video content. Your sole task is to translate the user's input accurately and naturally. 
Rules:
1. Translate the user's input as provided, treating it as the source text.
2. Use official translations for actor/actress names if available; otherwise, keep them unchanged.
3. Do not invent translations for names without official versions.
4. Maintain any numbers, dates, and measurements in their original format.
5. Translate naturally and fluently, avoiding word-for-word translation.
6. Do not add any explanations, notes, or comments under any circumstances.
7. Only output the translation result, with no additional content.`

// DefaultMaxCompletionTokens is used when no max completion tokens is configured.
// Reasoning-capable models may spend a large amount of tokens on hidden
// reasoning, so a generous default keeps long translations from being truncated.
const DefaultMaxCompletionTokens = 4096

type OpenAI struct {
	APIKey string `json:"openai-api-key"`
	APIUrl string `json:"openai-api-url"`
	Model  string `json:"openai-model"`
	Prompt string `json:"openai-prompt"`
	// DisableThinking turns off the reasoning/thinking mode for models
	// that support it (e.g. GLM, Qwen). It injects
	// "thinking": {"type": "disabled"} into the request body.
	DisableThinking bool `json:"openai-disable-thinking"`
	// MaxCompletionTokens bounds the number of generated tokens. Values
	// <= 0 fall back to DefaultMaxCompletionTokens.
	MaxCompletionTokens int `json:"openai-max-completion-tokens"`
}

func (oa *OpenAI) Translate(q, source, target string) (result string, err error) {
	config := openai.DefaultConfig(oa.APIKey)
	if oa.APIUrl != "" {
		config.BaseURL = oa.APIUrl
	}
	if oa.DisableThinking {
		config.HTTPClient = &thinkingDisabledClient{inner: config.HTTPClient}
	}

	maxCompletionTokens := oa.MaxCompletionTokens
	if maxCompletionTokens <= 0 {
		maxCompletionTokens = DefaultMaxCompletionTokens
	}

	systemPrompt := oa.Prompt
	if systemPrompt == "" {
		systemPrompt = defaultSystemPrompt
	}

	// Mimic the upstream prompt layout: an instruction message
	// followed by the source text as a separate message.
	assistantPrompt := "Please translate the following text"
	if lang := lookupLanguage(source); lang == "" || lang == "auto" {
		assistantPrompt += fmt.Sprintf(" into %s:", lookupLanguage(target))
	} else {
		assistantPrompt += fmt.Sprintf(" from %s to %s:", lang, lookupLanguage(target))
	}

	resp, err := openai.NewClientWithConfig(config).CreateChatCompletion(
		context.Background(),
		openai.ChatCompletionRequest{
			Model:               oa.Model,
			MaxCompletionTokens: maxCompletionTokens,
			Messages: []openai.ChatCompletionMessage{
				{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
				{Role: openai.ChatMessageRoleUser, Content: assistantPrompt},
				{Role: openai.ChatMessageRoleUser, Content: q},
			},
		},
	)
	if err != nil {
		return "", err
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("openai: empty response choices")
	}
	if reason := resp.Choices[0].FinishReason; reason == openai.FinishReasonLength {
		// Surface truncation instead of silently returning partial text.
		return "", fmt.Errorf("openai: response truncated by max_completion_tokens (%d)", maxCompletionTokens)
	}
	return resp.Choices[0].Message.Content, nil
}

// thinkingDisabledClient injects the vendor-specific "thinking" switch into
// the JSON request body. go-openai's ChatCompletionRequest has no generic
// extension field, so the body is patched on the wire instead.
type thinkingDisabledClient struct {
	inner openai.HTTPDoer
}

func (c *thinkingDisabledClient) Do(req *http.Request) (*http.Response, error) {
	if req.Body == nil || req.Header.Get("Content-Type") != "application/json" {
		return c.inner.Do(req)
	}

	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}

	var payload map[string]json.RawMessage
	if err = json.Unmarshal(body, &payload); err != nil {
		// Not a JSON object we understand; forward the original bytes untouched.
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		return c.inner.Do(req)
	}

	if _, exists := payload["thinking"]; !exists {
		payload["thinking"] = json.RawMessage(`{"type":"disabled"}`)
		if body, err = json.Marshal(payload); err != nil {
			return nil, err
		}
	}

	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	return c.inner.Do(req)
}

// languageNames holds codes whose display name differs from what the
// language package would produce (or that it cannot parse at all).
var languageNames = map[string]string{
	"wyw": "中文（古文-文言文）",
	"chs": "Simplified Chinese",
	"cht": "Traditional Chinese",
}

// lookupLanguage resolves a language code to an English display name,
// falling back to the original code when it cannot be parsed.
func lookupLanguage(code string) string {
	if code == "" {
		return ""
	}
	if name, ok := languageNames[strings.ToLower(code)]; ok {
		return name
	}
	tag, err := language.Parse(code)
	if err != nil {
		return code
	}
	return display.Tags(language.English).Name(tag)
}

func init() {
	translate.Register(&OpenAI{})
}
