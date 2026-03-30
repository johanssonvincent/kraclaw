package orchestrator

// oauthAllowedModels lists Claude API model IDs allowed in OAuth-only mode.
// Source: https://platform.claude.com/docs/en/about-claude/models/overview
var oauthAllowedModels = []modelInfo{
	{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
	{ID: "claude-haiku-4-5-20251001", DisplayName: "Claude Haiku 4.5"},
	{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5 (alias)"},
	{ID: "claude-sonnet-4-5-20250929", DisplayName: "Claude Sonnet 4.5"},
	{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5 (alias)"},
	{ID: "claude-opus-4-5-20251101", DisplayName: "Claude Opus 4.5"},
	{ID: "claude-opus-4-5", DisplayName: "Claude Opus 4.5 (alias)"},
	{ID: "claude-opus-4-1-20250805", DisplayName: "Claude Opus 4.1"},
	{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1 (alias)"},
	{ID: "claude-sonnet-4-20250514", DisplayName: "Claude Sonnet 4"},
	{ID: "claude-sonnet-4-0", DisplayName: "Claude Sonnet 4 (alias)"},
	{ID: "claude-opus-4-20250514", DisplayName: "Claude Opus 4"},
	{ID: "claude-opus-4-0", DisplayName: "Claude Opus 4 (alias)"},
	{ID: "claude-3-haiku-20240307", DisplayName: "Claude Haiku 3"},
}

func defaultOAuthAllowedModels() []modelInfo {
	return append([]modelInfo(nil), oauthAllowedModels...)
}
