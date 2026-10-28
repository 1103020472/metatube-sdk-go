package openai

import (
	"fmt"
	"strings"

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

// Names of the OpenAI client implementations that can be selected through the
// "openai-translator" request parameter.
const (
	// TranslatorOpenAITranslator is the legacy implementation built on
	// github.com/xjasonlyu/openai-translator. It is the default so that
	// existing deployments keep their previous behavior.
	TranslatorOpenAITranslator = "openai-translator"

	// TranslatorGoOpenAI talks to the OpenAI-compatible endpoint through
	// github.com/sashabaranov/go-openai directly. Only this implementation
	// honors ExtraParams and MaxCompletionTokens.
	TranslatorGoOpenAI = "go-openai"
)

// Chat message layouts. Local translation-only models (Hunyuan-MT / hy-mt2,
// NLLB-style fine-tunes, ...) are not chat models: their Ollama template has no
// system slot, so a system message either gets folded into the user turn or
// derails the model into writing an explanation instead of a translation.
const (
	// PromptModeDefault keeps the historical layout: a system message, an
	// instruction message and the source text as three separate messages.
	PromptModeDefault = "default"

	// PromptModeSingle sends exactly one user message holding the instruction
	// followed by the source text, and omits the system message entirely.
	// This is the layout Hunyuan-MT expects.
	PromptModeSingle = "single"

	// PromptModeSystemSingle keeps the system message but merges instruction
	// and source text into a single user message. Useful for chat models that
	// split badly when the source text arrives in its own turn.
	PromptModeSystemSingle = "system-single"
)

// defaultSingleInstruction is the phrasing Hunyuan-MT was trained on. Chinese
// instructions make hy-mt2 return an empty completion, so keep it English.
const defaultSingleInstruction = "Translate the following segment into {target}, without additional explanation."

type OpenAI struct {
	APIKey string `json:"openai-api-key"`
	APIUrl string `json:"openai-api-url"`
	Model  string `json:"openai-model"`
	Prompt string `json:"openai-prompt"`

	// Translator selects which OpenAI client implementation is used.
	// Empty or "openai-translator" (default) keeps the legacy
	// xjasonlyu/openai-translator behavior; "go-openai" switches to
	// sashabaranov/go-openai and enables ExtraParams / MaxCompletionTokens.
	Translator string `json:"openai-translator"`

	// ExtraParams is a raw JSON object merged into the chat completion
	// request body. Different OpenAI-compatible gateways need different
	// switches to curb reasoning, e.g.
	//   SiliconFlow: {"thinking_budget": 1}   (enable_thinking:false is ignored)
	//   Zhipu:       {"thinking": {"type": "disabled"}}
	//   vLLM/Qwen:   {"chat_template_kwargs": {"enable_thinking": false}}
	// It is ignored by the openai-translator implementation.
	ExtraParams string `json:"openai-extra-params"`

	// MaxCompletionTokens bounds the number of generated tokens. Values
	// <= 0 fall back to DefaultMaxCompletionTokens. It is ignored by the
	// openai-translator implementation.
	MaxCompletionTokens int `json:"openai-max-completion-tokens"`

	// PromptMode selects the chat message layout: "default" (system +
	// instruction + text, the historical behavior), "single" (one merged user
	// message, no system message) or "system-single" (system + one merged user
	// message). Non-default modes always run on the go-openai implementation
	// because the legacy library cannot control the message layout.
	PromptMode string `json:"openai-prompt-mode"`

	// Instruction overrides the instruction line sent to the model. It may
	// reference {target} and {source} (English language names); the source text
	// is always appended afterwards and must not be included here. Empty means
	// the built-in wording for the active PromptMode.
	Instruction string `json:"openai-instruction"`

	// ReasoningEffort is forwarded as the OpenAI "reasoning_effort" field.
	// Hybrid-reasoning models (Qwen3 and later) spend the whole token budget on
	// hidden reasoning and return an empty content otherwise, so "none" is
	// required to make them behave like plain translators.
	ReasoningEffort string `json:"openai-reasoning-effort"`

	// Temperature is forwarded as the OpenAI "temperature" field. It is a
	// pointer so that an explicit 0 (greedy decoding) is distinguishable from
	// "not configured": local models at their default temperature return a
	// different answer on every call, which makes actress names and tags
	// unusable. Set 0 for reproducible output.
	Temperature *float64 `json:"openai-temperature"`
}

// Translate dispatches to the configured OpenAI client implementation.
func (oa *OpenAI) Translate(q, source, target string) (string, error) {
	kind, err := resolveTranslator(oa.Translator)
	if err != nil {
		return "", err
	}
	mode, err := resolvePromptMode(oa.PromptMode)
	if err != nil {
		return "", err
	}
	// The legacy library hard-codes its own message layout and temperature
	// (0.1), so these settings can only be honored on the go-openai path.
	// Upgrade silently rather than ignoring them, which would look like a
	// broken configuration.
	if kind == TranslatorGoOpenAI || mode != PromptModeDefault || oa.Temperature != nil {
		return oa.translateWithGoOpenAI(q, source, target, mode)
	}
	return oa.translateWithOpenAITranslator(q, source, target)
}

// resolvePromptMode validates and canonicalizes the configured layout.
func resolvePromptMode(name string) (string, error) {
	switch normalizeTranslatorName(name) {
	case "", "default", "chat":
		return PromptModeDefault, nil
	case "single", "usersingle":
		return PromptModeSingle, nil
	case "systemsingle", "system":
		return PromptModeSystemSingle, nil
	default:
		return "", fmt.Errorf("openai: unknown prompt mode %q (supported: %q, %q, %q)",
			name, PromptModeDefault, PromptModeSingle, PromptModeSystemSingle)
	}
}

// resolveTranslator validates and canonicalizes the configured translator name.
// An empty value means the default (openai-translator). Unknown values are
// rejected instead of silently falling back, so a typo cannot quietly make
// ExtraParams / MaxCompletionTokens ineffective.
func resolveTranslator(name string) (string, error) {
	switch normalizeTranslatorName(name) {
	case "", "openaitranslator":
		return TranslatorOpenAITranslator, nil
	case "goopenai":
		return TranslatorGoOpenAI, nil
	default:
		return "", fmt.Errorf("openai: unknown translator %q (supported: %q, %q)",
			name, TranslatorOpenAITranslator, TranslatorGoOpenAI)
	}
}

// normalizeTranslatorName makes the config tolerant to casing and separators,
// e.g. "Go_OpenAI" and "go openai" both normalize to "goopenai".
func normalizeTranslatorName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	return strings.NewReplacer("-", "", "_", "", " ", "").Replace(name)
}

func init() {
	translate.Register(&OpenAI{})
}
