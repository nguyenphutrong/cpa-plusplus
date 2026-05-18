package responses

import (
	. "github.com/nguyenphutrong/cpa-plusplus/v7/internal/constant"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/interfaces"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Gemini,
		ConvertOpenAIResponsesRequestToGemini,
		interfaces.TranslateResponse{
			Stream:    ConvertGeminiResponseToOpenAIResponses,
			NonStream: ConvertGeminiResponseToOpenAIResponsesNonStream,
		},
	)
}
