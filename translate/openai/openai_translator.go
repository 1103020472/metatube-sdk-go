package openai

import (
	translator "github.com/xjasonlyu/openai-translator"
)

// translateWithOpenAITranslator is the legacy translation path built on
// github.com/xjasonlyu/openai-translator. It is kept byte-for-byte equivalent
// to the previous behavior (including its own prompt layout, language lookup
// and default max_completion_tokens). ExtraParams and MaxCompletionTokens are
// intentionally not applied here because the upstream library cannot forward
// arbitrary request-body fields.
func (oa *OpenAI) translateWithOpenAITranslator(q, source, target string) (string, error) {
	var opts []translator.TranslatorOption
	if oa.APIUrl != "" {
		opts = append(opts, translator.WithBaseURL(oa.APIUrl))
	}
	return translator.
		NewTranslator(oa.APIKey, opts...).
		TranslateText(q, target,
			translator.WithModel(oa.Model),
			translator.WithSourceLanguage(source),
			translator.WithSystemPrompt(map[bool]string{
				true:  oa.Prompt,
				false: defaultSystemPrompt,
			}[oa.Prompt != ""]),
		)
}
