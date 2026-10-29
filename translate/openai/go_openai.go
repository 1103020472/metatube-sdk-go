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
	"unicode/utf8"

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
	return validateResult(stripControlTokens(resp.Choices[0].Message.Content), target)
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

	known := sourceName != "" && sourceName != "auto"

	if mode == PromptModeDefault {
		if !known {
			return fmt.Sprintf("Please translate the following text into %s:", targetName)
		}
		return fmt.Sprintf("Please translate the following text from %s to %s:", sourceName, targetName)
	}

	if !known {
		return strings.ReplaceAll(defaultSingleInstructionNoSource, "{target}", targetName)
	}
	return strings.NewReplacer(
		"{target}", targetName,
		"{source}", sourceName,
	).Replace(defaultSingleInstruction)
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

// Language-drift detection.
//
// A local model occasionally loses track of the requested target language and
// keeps writing in the source language: asked for Chinese it returns the
// Japanese input lightly reworded (「」→『』, ４→4, sentences paraphrased).
// The result is not empty and not a canned reply, so nothing downstream would
// notice — the untranslated block simply gets written into the media library.
// These helpers reject that output so the caller can retry or fall back.
var (
	// kanaRe matches hiragana and katakana. They are unique to Japanese:
	// Chinese output never contains them apart from stray proper nouns.
	kanaRe = regexp.MustCompile(`[\x{3040}-\x{309F}\x{30A0}-\x{30FF}]`)
	// letterRe counts the letters a line is actually made of, so the kana
	// ratio is not diluted by punctuation, digits or HTML.
	letterRe = regexp.MustCompile(`[0-9A-Za-z\x{3040}-\x{309F}\x{30A0}-\x{30FF}\x{4E00}-\x{9FFF}]`)
	tagRe    = regexp.MustCompile(`<[^>]*>`)
	// lineSplitRe splits the completion the way the plugin renders it.
	lineSplitRe = regexp.MustCompile(`<br\s*/?>|\r?\n`)
)

const (
	// maxKanaRatio is the kana share above which a line counts as Japanese.
	// Genuine Chinese translations sit below 0.05; a drifted line sits at 0.5+.
	maxKanaRatio = 0.3
	// minRunesForDriftCheck skips short fragments: names, tags, dates.
	minRunesForDriftCheck = 15
)

// looksUntranslated reports whether one line of the completion is still
// written in Japanese.
func looksUntranslated(line string) bool {
	text := strings.TrimSpace(tagRe.ReplaceAllString(line, ""))
	// Author lines keep the reviewer's nickname verbatim, kana included.
	if strings.HasPrefix(text, "👤") {
		return false
	}
	if utf8.RuneCountInString(text) < minRunesForDriftCheck {
		return false
	}
	letters := letterRe.FindAllString(text, -1)
	if len(letters) == 0 {
		return false
	}
	return float64(len(kanaRe.FindAllString(text, -1)))/float64(len(letters)) > maxKanaRatio
}

// isChineseTarget reports whether the target is any Chinese variant.
func isChineseTarget(target string) bool {
	t := strings.ToLower(strings.TrimSpace(target))
	if t == "" {
		return false
	}
	if _, ok := languageNames[t]; ok {
		return strings.HasPrefix(t, "zh") || t == "chs" || t == "cht" || t == "wyw"
	}
	return strings.HasPrefix(t, "zh")
}

// detectLanguageDrift reports whether any line of the completion still looks
// like Japanese while the caller asked for Chinese.
func detectLanguageDrift(s, target string) bool {
	if !isChineseTarget(target) {
		return false
	}
	for _, line := range lineSplitRe.Split(s, -1) {
		if looksUntranslated(line) {
			return true
		}
	}
	return false
}

// validateResult rejects empty completions, canned non-translations and
// output that never left the source language.
func validateResult(s, target string) (string, error) {
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
	if detectLanguageDrift(s, target) {
		return "", fmt.Errorf(
			"openai: output is still in the source language despite target %q; refusing to write an untranslated result",
			target)
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
//
// The zh-* entries matter twice over:
//   - display.Tags renders zh-CN as "Chinese (China)", and translation-only
//     models (Hunyuan-MT / hy-mt2) were trained on the bare "Chinese" phrasing,
//     so the region suffix has to go;
//   - the bare word "Chinese" is nonetheless ambiguous for chat models. Asked to
//     translate a long Japanese paragraph into "Chinese", qwen3.5 sometimes
//     rewrites it in Japanese instead. Naming the script ("Simplified Chinese")
//     removes that ambiguity, so every zh-* variant maps to a script name.
//
// Bare "zh" stays "Chinese": it genuinely does not say which script is wanted.
var languageNames = map[string]string{
	"wyw":     "中文（古文-文言文）",
	"chs":     "Simplified Chinese",
	"cht":     "Traditional Chinese",
	"zh":      "Chinese",
	"zh-cn":   "Simplified Chinese",
	"zh-hans": "Simplified Chinese",
	"zh-sg":   "Simplified Chinese",
	"zh-my":   "Simplified Chinese",
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
