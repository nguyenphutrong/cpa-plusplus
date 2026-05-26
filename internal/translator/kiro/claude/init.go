package claude

import (
	"context"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/translator"
)

func init() {
	translator.Register(
		Claude,
		Kiro,
		ConvertClaudeRequestToKiro,
		interfaces.TranslateResponse{
			Stream:     ConvertKiroStreamToClaude,
			NonStream:  ConvertKiroNonStreamToClaude,
			TokenCount: ClaudeTokenCount,
		},
	)
}

func ClaudeTokenCount(_ context.Context, count int64) []byte {
	return translatorcommon.ClaudeInputTokensJSON(count)
}
