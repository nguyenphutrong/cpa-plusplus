package responses

import (
	. "github.com/nguyenphutrong/cpa-plusplus/v7/internal/constant"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/interfaces"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenaiResponse,
		Antigravity,
		ConvertOpenAIResponsesRequestToAntigravity,
		interfaces.TranslateResponse{
			Stream:    ConvertAntigravityResponseToOpenAIResponses,
			NonStream: ConvertAntigravityResponseToOpenAIResponsesNonStream,
		},
	)
}
