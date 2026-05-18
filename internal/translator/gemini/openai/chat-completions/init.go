package chat_completions

import (
	. "github.com/nguyenphutrong/cpa-plusplus/v7/internal/constant"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/interfaces"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Gemini,
		ConvertOpenAIRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToOpenAI,
			NonStream: ConvertGeminiResponseToOpenAINonStream,
		},
	)
}
