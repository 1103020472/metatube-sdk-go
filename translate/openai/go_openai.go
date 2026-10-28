package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	openai "github.com/sashabaranov/go-openai"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// DefaultMaxCompletionTokens is used when no max completion tokens is configured.
// Reasoning-capable models may spend a large amount of tokens on hidden
// reasoning, so a generous default keeps long translations from being truncated.
const DefaultMaxCompletionTokens = 4096

// translateWithGoOpenAI calls the OpenAI-compatible endpoint with
// github.com/sashabaranov/go-openai directly. Compared with the legacy
// openai-translator path it can forward vendor-specific request-body fields
// (ExtraParams), an explicit max_completion_tokens bound, a reasoning_effort
// hint and a caller-chosen message layout (PromptMode).
func (oa *OpenAI) translateWithGoOpenAI(q, source, target, mode string) (string, error) {
	config := openai.DefaultConfig(oa.APIKey)
	if oa.APIUrl != "" {
		config.BaseURL = oa.APIUrl
	}

	var extra map[string]json.RawMessage
	if strings.TrimSpace(oa.ExtraParams) != "" {
		if err := json.Unmarshal([]byte(oa.ExtraParams), &extra); err != nil {
			return "", fmt.Errorf("openai: invalid extra params JSON: %w", err)
		}
	}

	// go-openai tags Temperature with omitempty, so 0 never reaches the wire on
	// its own. Fold it into the patched body; when the same key also appears in
	// ExtraParams, this dedicated field wins.
	if oa.Temperature != nil {
		if extra == nil {
			extra = make(map[string]json.RawMessage, 1)
		}
		extra["temperature"] = json.RawMessage(strconv.FormatFloat(*oa.Temperature, 'f', -1, 64))
	}

	if len(extra) > 0 {
		config.HTTPClient = &bodyPatchClient{inner: config.HTTPClient, extra: extra}
	}

	maxCompletionTokens := oa.MaxCompletionTokens
	if maxCompletionTokens <= 0 {
		maxCompletionTokens = DefaultMaxCompletionTokens
	}

	req := openai.ChatCompletionRequest{
		Model:               oa.Model,
		MaxCompletionTokens: maxCompletionTokens,
		Messages:            oa.buildMessages(q, source, target, mode),
	}
	if effort := strings.TrimSpace(oa.ReasoningEffort); effort != "" {
		req.ReasoningEffort = effort
	}

	resp, err := openai.NewClientWithConfig(config).CreateChatCompletion(context.Background(), req)
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
	return validateResult(stripControlTokens(resp.Choices[0].Message.Content))
}

// buildMessages renders the request messages for the configured layout.
func (oa *OpenAI) buildMessages(q, source, target, mode string) []openai.ChatCompletionMessage {
	instruction := oa.renderInstruction(source, target, mode)
	switch mode {
	case PromptModeSingle:
		// No system message at all: translation-only models such as hy-mt2
		// have no system slot and will start explaining the text instead of
		// translating it.
		return []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleUser, Content: instruction + "\n\n" + q},
		}
	case PromptModeSystemSingle:
		return []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: oa.systemPrompt()},
			{Role: openai.ChatMessageRoleUser, Content: instruction + "\n\n" + q},
		}
	default:
		// Mimic the upstream prompt layout: an instruction message
		// followed by the source text as a separate message.
		return []openai.ChatCompletionMessage{
			{Role: openai.ChatMessageRoleSystem, Content: oa.systemPrompt()},
			{Role: openai.ChatMessageRoleUser, Content: instruction},
			{Role: openai.ChatMessageRoleUser, Content: q},
		}
	}
}

// systemPrompt returns the configured system prompt, or the built-in default.
func (oa *OpenAI) systemPrompt() string {
	if strings.TrimSpace(oa.Prompt) != "" {
		return oa.Prompt
	}
	return defaultSystemPrompt
}

// renderInstruction produces the instruction line for the given layout.
// The source text is never part of it; callers append it themselves.
func (oa *OpenAI) renderInstruction(source, target, mode string) string {
	targetName := lookupLanguage(target)
	sourceName := lookupLanguage(source)

	if custom := strings.TrimSpace(oa.Instruction); custom != "" {
		return strings.NewReplacer(
			"{target}", targetName,
			"{source}", sourceName,
		).Replace(custom)
	}

	if mode == PromptModeDefault {
		if sourceName == "" || sourceName == "auto" {
			return fmt.Sprintf("Please translate the following text into %s:", targetName)
		}
		return fmt.Sprintf("Please translate the following text from %s to %s:", sourceName, targetName)
	}
	return strings.ReplaceAll(defaultSingleInstruction, "{target}", targetName)
}

// controlTokenRe matches leaked model control tokens such as <|hy-Assistant|>,
// <｜hy_begin▁of▁sentence｜> or their byte-mangled variants.
var controlTokenRe = regexp.MustCompile(`<[^<>]{0,3}[|｜][^<>]{0,40}[|｜]>`)

// stripControlTokens removes control tokens that end up in the completion when
// a model's stop list does not match what the model actually emits. Some Ollama
// GGUFs ship the terminator with full-width bars while the weights emit ASCII
// ones, so the token survives and would otherwise be shown to users verbatim.
func stripControlTokens(s string) string {
	s = controlTokenRe.ReplaceAllString(s, "")
	// Truncate at the first dangling marker; these only ever appear as a
	// terminator, so anything after it is noise.
	for _, marker := range []string{"<|", "<｜", "｜hy_", "▁hy_"} {
		if i := strings.Index(s, marker); i >= 0 {
			s = s[:i]
		}
	}
	return strings.TrimSpace(s)
}

// nonTranslationReplies are canned "done" replies that translation-only models
// emit when the input does not look like a sentence (a bare list of actress
// names, for instance). Passing them through would silently poison the
// library, so they are surfaced as errors to let the caller fall back.
var nonTranslationReplies = []string{
	"已为您完成翻译",
	"已完成翻译",
	"翻译已完成",
	"已翻译完成",
}

// validateResult rejects empty completions and canned non-translations.
func validateResult(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("openai: empty translation result")
	}
	if len([]rune(s)) <= 20 {
		for _, canned := range nonTranslationReplies {
			if strings.Contains(s, canned) {
				return "", fmt.Errorf("openai: model returned a non-translation reply %q", s)
			}
		}
	}
	return s, nil
}

// bodyPatchClient merges extra JSON fields into the outgoing request body.
// go-openai's ChatCompletionRequest has no generic extension field, so
// vendor-specific switches (enable_thinking, thinking, ...) are injected
// on the wire instead. It is also how an explicit temperature of 0 survives:
// that field is tagged omitempty, so the struct never serializes it.
type bodyPatchClient struct {
	inner openai.HTTPDoer
	extra map[string]json.RawMessage
}

func (c *bodyPatchClient) Do(req *http.Request) (*http.Response, error) {
	if req.Body == nil {
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

	// Never overwrite a field the request already set explicitly.
	for key, value := range c.extra {
		if _, exists := payload[key]; !exists {
			payload[key] = value
		}
	}
	if body, err = json.Marshal(payload); err != nil {
		return nil, err
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
// The zh-* entries matter: display.Tags renders zh-CN as "Chinese (China)",
// while translation-only models (Hunyuan-MT / hy-mt2) were trained on the bare
// "Chinese" phrasing and degrade when given the region suffix.
var languageNames = map[string]string{
	"wyw":     "中文（古文-文言文）",
	"chs":     "Simplified Chinese",
	"cht":     "Traditional Chinese",
	"zh":      "Chinese",
	"zh-cn":   "Chinese",
	"zh-hans": "Chinese",
	"zh-sg":   "Chinese",
	"zh-my":   "Chinese",
	"zh-tw":   "Traditional Chinese",
	"zh-hk":   "Traditional Chinese",
	"zh-mo":   "Traditional Chinese",
	"zh-hant": "Traditional Chinese",
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
