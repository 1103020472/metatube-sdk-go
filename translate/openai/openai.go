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
}

// Translate dispatches to the configured OpenAI client implementation.
func (oa *OpenAI) Translate(q, source, target string) (string, error) {
	kind, err := resolveTranslator(oa.Translator)
	if err != nil {
		return "", err
	}
	if kind == TranslatorGoOpenAI {
		return oa.translateWithGoOpenAI(q, source, target)
	}
	return oa.translateWithOpenAITranslator(q, source, target)
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
