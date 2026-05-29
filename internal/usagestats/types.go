package usagestats

type Event struct {
	ID               int64
	RequestID        string
	EventHash        string
	TimestampMS      int64
	Provider         string
	Channel          string
	Model            string
	RequestedModel   string
	ResolvedModel    string
	Endpoint         string
	Method           string
	Path             string
	AuthType         string
	AuthIndex        string
	Account          string
	AccountHash      string
	APIKeyHash       string
	StatusCode       int
	PromptTokens     int64
	CompletionTokens int64
	ReasoningTokens  int64
	CachedTokens     int64
	CacheTokens      int64
	TotalTokens      int64
	LatencyMS        *int64
	Failed           bool
	CreatedAtMS      int64
}

type Tokens struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheTokens      int64 `json:"cache_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type ModelPrice struct {
	Prompt        float64 `json:"prompt"`
	Completion    float64 `json:"completion"`
	Cache         float64 `json:"cache"`
	Source        string  `json:"source,omitempty"`
	SourceModelID string  `json:"source_model_id,omitempty"`
	RawJSON       string  `json:"raw_json,omitempty"`
	UpdatedAtMS   int64   `json:"updated_at_ms,omitempty"`
	SyncedAtMS    *int64  `json:"synced_at_ms,omitempty"`
}

type InsertResult struct {
	Inserted int
	Skipped  int
}

type SummaryFilter struct {
	StartMS   *int64
	EndMS     *int64
	Account   string
	Model     string
	Channel   string
	AuthIndex string
}

type Summary struct {
	TotalRequests    int64   `json:"total_requests"`
	SuccessCount     int64   `json:"success_count"`
	FailureCount     int64   `json:"failure_count"`
	Tokens           Tokens  `json:"tokens"`
	LatencySumMS     int64   `json:"latency_sum_ms,omitempty"`
	LatencyCount     int64   `json:"latency_count,omitempty"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
}
