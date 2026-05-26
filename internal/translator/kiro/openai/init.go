package openai

import (
	"context"
	"strconv"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		OpenAI,
		Kiro,
		ConvertOpenAIRequestToKiro,
		interfaces.TranslateResponse{
			Stream:     ConvertKiroStreamToOpenAI,
			NonStream:  ConvertKiroNonStreamToOpenAI,
			TokenCount: OpenAITokenCount,
		},
	)
}

func OpenAITokenCount(_ context.Context, count int64) []byte {
	out := make([]byte, 0, 64)
	out = append(out, `{"usage":{"prompt_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `,"completion_tokens":0,"total_tokens":`...)
	out = strconv.AppendInt(out, count, 10)
	out = append(out, `}}`...)
	return out
}
