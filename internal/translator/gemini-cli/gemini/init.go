package gemini

import (
	. "github.com/nguyenphutrong/cpa-plusplus/v7/internal/constant"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/interfaces"
	"github.com/nguyenphutrong/cpa-plusplus/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Gemini,
		GeminiCLI,
		ConvertGeminiRequestToGeminiCLI,
		interfaces.TranslateResponse{
			Stream:     ConvertGeminiCliResponseToGemini,
			NonStream:  ConvertGeminiCliResponseToGeminiNonStream,
			TokenCount: GeminiTokenCount,
		},
	)
}
