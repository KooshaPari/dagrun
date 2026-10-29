package harness

// Usage is the token and cost accounting a harness reported, when it reports
// any. Every field is optional: harnesses differ in what they emit, and a
// missing number must never become a fabricated zero.
type Usage struct {
	Model            string `json:"model,omitempty"`
	Provider         string `json:"provider,omitempty"`
	InputTokens      int    `json:"input_tokens,omitempty"`
	OutputTokens     int    `json:"output_tokens,omitempty"`
	CacheReadTokens  int    `json:"cache_read_input_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_creation_input_tokens,omitempty"`
	TotalTokens      int    `json:"total_tokens,omitempty"`
	DurationMS       int64  `json:"duration_ms,omitempty"`
}

// Total is the token count when the harness gave one, otherwise the sum of the
// parts. It never guesses: a harness that reports nothing reports zero.
func (u Usage) Total() int {
	if u.TotalTokens > 0 {
		return u.TotalTokens
	}
	return u.InputTokens + u.OutputTokens
}
