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

	messages := oa.buildMessages(q, source, target, mode)

	out, err := oa.complete(config, messages, maxCompletionTokens)
	if err != nil {
		return "", err
	}

	// Safety-aligned models sometimes answer a translation request with a
	// refusal instead of a translation ("I can't provide content of that
	// nature"). The refusal is neither empty nor in the wrong language, so
	// every other check here lets it through and it would be written into the
	// library as if it were the translation.
	//
	// A neutral "machine translation engine" system message reliably stops it:
	// the same text that gets refused in a plain user turn is translated once
	// the system role says the job is mechanical. Measured on qwen3.5:9b, 14 of
	// 14 previously refused titles came back translated, and a 999-character
	// refused review came back as 780 characters of Chinese.
	//
	// Only the refusal path pays for this. Ordinary chunks keep their original
	// layout, which matters because an English system prompt is exactly the
	// attractor that made the model drift back into English in the first place.
	if isRefusalReply(out, q) {
		retry, retryErr := oa.complete(config, withNeutralSystemPrompt(messages), maxCompletionTokens)
		if retryErr != nil {
			return "", fmt.Errorf("%s (%q) and the retry failed: %w",
				refusalErrorPrefix, ellipsis(out, 60), retryErr)
		}
		if isRefusalReply(retry, q) {
			return "", fmt.Errorf("%s (%q)", refusalErrorPrefix, ellipsis(out, 60))
		}
		out = retry
	}

	return validateResult(out, target)
}

// refusalErrorPrefix opens every error this file returns when the model
// refuses, and the wording is a contract, not cosmetics.
//
// The Emby/Jellyfin plugin classifies translation errors by matching the
// exception text against
// "content[\s_-]*(policy|filter)|moderation|sensitive|safety|prohibited|..." to
// decide whether an error is a content problem or an endpoint problem. That
// distinction drives two different behaviours:
//
//   - content problem -> the local channel is abandoned without starting its
//     failure cooldown, and the primary channel switches to the
//     sensitive-content fallback (B) instead of failing the whole title.
//   - endpoint problem -> the local channel cools down for N minutes and the
//     error is retried/failed as a transport problem.
//
// A refusal is unambiguously a content problem, so the prefix says "content
// policy". Without it the plugin reads a refusal as an endpoint failure: one
// refused title benches the local provider for ten minutes, and a refusal from
// the primary provider kills the translation instead of trying the B channel.
const refusalErrorPrefix = "openai: content policy refusal: the model refused to translate"

// complete performs one chat completion and returns the cleaned content.
func (oa *OpenAI) complete(config openai.ClientConfig, messages []openai.ChatCompletionMessage,
	maxCompletionTokens int) (string, error) {
	req := openai.ChatCompletionRequest{
		Model:               oa.Model,
		MaxCompletionTokens: maxCompletionTokens,
		Messages:            messages,
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
	return stripControlTokens(resp.Choices[0].Message.Content), nil
}

// refusalRetrySystemPrompt is used only when the model refused a request.
//
// Deliberately free of any "you must not refuse" wording: telling a model to
// ignore its own rules is both unnecessary here and the kind of instruction a
// future model may treat as an attack. Describing the task as mechanical
// translation, with the formatting constraints the caller actually needs, is
// enough to keep it on task.
const refusalRetrySystemPrompt = `You are a machine translation engine. ` +
	`Translate the given text verbatim into the requested language and output nothing else. ` +
	`Preserve HTML tags, the author line and any date or number exactly as written.`

// withNeutralSystemPrompt puts refusalRetrySystemPrompt in the system slot,
// dropping any system message the original layout had. Everything else keeps
// its position so the source text still arrives the way the model expects.
func withNeutralSystemPrompt(messages []openai.ChatCompletionMessage) []openai.ChatCompletionMessage {
	out := make([]openai.ChatCompletionMessage, 0, len(messages)+1)
	out = append(out, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleSystem,
		Content: refusalRetrySystemPrompt,
	})
	for _, m := range messages {
		if m.Role == openai.ChatMessageRoleSystem {
			continue
		}
		out = append(out, m)
	}
	return out
}

// refusalRe matches the shape of a refusal rather than one exact sentence.
// A fixed phrase list does not survive contact with reality: the same model
// answered the same kind of input with "我无法提供包含色情内容的信息",
// "无法提供该请求的翻译。作为 AI 助手，我严格遵守内容安全规范" and
// "无法提供该内容的翻译" within one afternoon. What stays constant is that a
// refusal talks about the request or about the assistant, not about the source
// text: a first-person inability, an AI self-reference, or an inability to
// provide "this/your" content.
//
// The subjectless form is deliberately split out and anchored to the start of
// the reply (refusalHeadRe). "无法提供该商品的库存" is a perfectly ordinary
// sentence inside a translation; a refusal opens with it.
var refusalRe = regexp.MustCompile(`(?i)` +
	`作为\s*(AI|人工智能|语言模型|智能助手)` + `|` +
	`AI\s*助手|人工智能助手|语言模型` + `|` +
	`我\s*(无法|不能|不可以|不便|拒绝)(提供|协助|翻译|满足|完成|处理|参与|生成)` + `|` +
	`抱歉[，,]\s*我(无法|不能)` + `|` +
	// Gateway-side moderation replies (one-api / new-api style) are neither a
	// model refusal nor a translation; they arrive as ordinary content.
	`系统检测到.{0,40}敏感` + `|` +
	`as an ai|i\s*(am|'m)\s*(sorry|unable|afraid)` + `|` +
	`i\s*(can't|cannot|can not|won't|will not|am unable to)\s*` +
	`(help|assist|translate|provide|comply|respond|answer|continue|do)`)

var refusalHeadRe = regexp.MustCompile(`^(无法|不能)(提供|协助|翻译)(该|此|您)`)

const (
	// maxRefusalRunes bounds the pattern check. Refusals are one or two
	// sentences; a real translation of a long chunk is far longer, so a phrase
	// quoted inside a long translation cannot be mistaken for a refusal.
	maxRefusalRunes = 200

	// maxRefusalHeadRunes bounds the subjectless form separately: it is a much
	// weaker signal, so only a short reply is allowed to use it.
	maxRefusalHeadRunes = 120

	// maxRefusalTailRunes bounds the trailing-window check. A model does not
	// always replace the translation with a refusal: it can translate the text
	// correctly and then append a refusal sentence of its own
	// ("…榨不出精子为止。\nSorry, I can't respond to this question."). The reply
	// is long and mostly a real translation, so the whole-text checks above
	// cannot see the refusal, and the drift check below reports it as
	// untranslated output — which the plugin reads as an endpoint problem and
	// answers by benching the provider instead of routing to the
	// sensitive-content fallback. Only the tail can tell the two apart.
	maxRefusalTailRunes = 160

	// minSourceRunesForShrinkCheck and shrinkDivisor catch the refusal that
	// carries no marker at all: a long chunk that comes back as a handful of
	// characters. A 999-character review refused with "无法提供。" (5 runes).
	// No legitimate translation shrinks by an order of magnitude.
	minSourceRunesForShrinkCheck = 300
	shrinkDivisor                = 10
)

// refusalInTail reports whether a refusal marker sits in the last
// maxRefusalTailRunes of text. Scanning a window rather than whole lines keeps
// a phrase quoted in the middle of a long translation from matching, while
// still catching the appended-refusal case whether the model put it on a new
// line or glued it to the last sentence.
func refusalInTail(text string) bool {
	r := []rune(text)
	if len(r) > maxRefusalTailRunes {
		text = string(r[len(r)-maxRefusalTailRunes:])
	}
	return refusalRe.MatchString(text)
}

// isRefusalReply reports whether the completion is a refusal rather than a
// translation of source.
func isRefusalReply(reply, source string) bool {
	text := strings.TrimSpace(reply)
	if text == "" {
		return false // handled separately as an empty result
	}
	replyRunes := utf8.RuneCountInString(text)

	if replyRunes <= maxRefusalRunes && refusalRe.MatchString(text) {
		return true
	}
	if replyRunes <= maxRefusalHeadRunes && refusalHeadRe.MatchString(text) {
		return true
	}
	// Longer replies are checked by their tail: a valid translation with a
	// refusal appended is still a refusal.
	if replyRunes > maxRefusalRunes && refusalInTail(text) {
		return true
	}

	sourceRunes := utf8.RuneCountInString(source)
	return sourceRunes >= minSourceRunesForShrinkCheck && replyRunes*shrinkDivisor < sourceRunes
}

// ellipsis shortens s for inclusion in an error message.
func ellipsis(s string, limit int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
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
//
// Two different smells are checked, because the two languages need different
// evidence:
//
//   - Japanese is easy: kana only ever show up in Japanese text, so a line
//     that is 30% kana is untranslated with near-certainty.
//   - English is not: latin letters are perfectly normal inside a Chinese
//     translation ("DASD-693", "F罩杯", romanized actress names), so a latin
//     ratio would flag half the library. The check therefore requires the line
//     to look like an English *sentence* — no Chinese characters at all, plus
//     several function words (the/is/and/...), which a list of names or a spec
//     line never contains.
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
	// hanRe detects CJK ideographs: any of them means the line was translated.
	hanRe = regexp.MustCompile(`[\x{4E00}-\x{9FFF}]`)
	// wordRe pulls latin words out of a line.
	wordRe = regexp.MustCompile(`[A-Za-z]+`)
)

const (
	// maxKanaRatio is the kana share above which a line counts as Japanese.
	// Genuine Chinese translations sit below 0.05; a drifted line sits at 0.5+.
	maxKanaRatio = 0.3
	// minRunesForDriftCheck skips short fragments: names, tags, dates.
	minRunesForDriftCheck = 15
	// minRunesForEnglishDrift is deliberately longer than the Japanese
	// threshold: a drifted English paragraph is long, while the short latin
	// fragments that legitimately survive translation are not.
	minRunesForEnglishDrift = 25
	// minEnglishWords keeps name lists ("Mihashi Ren, Yua Mikami") out.
	minEnglishWords = 5
	// minEnglishStopWords is what separates prose from a list of names.
	minEnglishStopWords = 2
)

// englishStopWords are high-frequency function words. They appear in almost
// any English sentence and essentially never in a list of romanized names,
// a catalogue number or a resolution tag.
var englishStopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"but": {}, "by": {}, "can": {}, "do": {}, "for": {}, "from": {}, "had": {},
	"has": {}, "have": {}, "he": {}, "her": {}, "his": {}, "how": {}, "i": {},
	"if": {}, "in": {}, "is": {}, "it": {}, "its": {}, "just": {}, "me": {},
	"my": {}, "no": {}, "not": {}, "of": {}, "on": {}, "or": {}, "she": {},
	"so": {}, "that": {}, "the": {}, "their": {}, "them": {}, "then": {},
	"there": {}, "these": {}, "they": {}, "this": {}, "to": {}, "too": {},
	"very": {}, "was": {}, "we": {}, "were": {}, "what": {}, "when": {},
	"which": {}, "who": {}, "will": {}, "with": {}, "would": {}, "you": {},
	"your": {},
}

// lineText strips the HTML tags and reports whether the line is exempt from
// drift checks: reviewer nicknames are kept verbatim by the prompt, kana and
// all.
func lineText(line string) (string, bool) {
	text := strings.TrimSpace(tagRe.ReplaceAllString(line, ""))
	return text, strings.HasPrefix(text, "👤")
}

// looksUntranslated reports whether one line of the completion is still
// written in Japanese.
func looksUntranslated(text string) bool {
	if utf8.RuneCountInString(text) < minRunesForDriftCheck {
		return false
	}
	letters := letterRe.FindAllString(text, -1)
	if len(letters) == 0 {
		return false
	}
	return float64(len(kanaRe.FindAllString(text, -1)))/float64(len(letters)) > maxKanaRatio
}

// looksLikeEnglishProse reports whether one line is still written in English.
// It is intentionally conservative: a false positive here fails the whole
// chunk (the plugin treats a rejected chunk as a failed one), which is worse
// than letting a rare English line through.
func looksLikeEnglishProse(text string) bool {
	if utf8.RuneCountInString(text) < minRunesForEnglishDrift {
		return false
	}
	// A single Chinese character means the line was translated: Chinese output
	// legitimately carries latin fragments (DASD-693, F罩杯, 4K).
	if hanRe.MatchString(text) {
		return false
	}

	words := wordRe.FindAllString(text, -1)
	if len(words) < minEnglishWords {
		return false
	}

	stop := 0
	for _, word := range words {
		if _, ok := englishStopWords[strings.ToLower(word)]; ok {
			stop++
			if stop >= minEnglishStopWords {
				return true
			}
		}
	}
	return false
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

// detectLanguageDrift reports whether any line of the completion is still in
// the source language (or slipped into English) while the caller asked for
// Chinese.
func detectLanguageDrift(s, target string) bool {
	if !isChineseTarget(target) {
		return false
	}
	for _, line := range lineSplitRe.Split(s, -1) {
		text, exempt := lineText(line)
		if exempt {
			continue
		}
		if looksUntranslated(text) || looksLikeEnglishProse(text) {
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
			"openai: output is still in the source language or in English despite target %q; refusing to write an untranslated result",
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
