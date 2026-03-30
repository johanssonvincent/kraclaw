package orchestrator

// oauthAllowedModels lists Claude API model aliases allowed in OAuth-only mode.
// Only aliases are listed — dated IDs are accepted by the API but not shown to users.
// Source: https://platform.claude.com/docs/en/about-claude/models/overview
var oauthAllowedModels = []modelInfo{
	{ID: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
	{ID: "claude-sonnet-4-6", DisplayName: "Claude Sonnet 4.6"},
	{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5"},
	{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5"},
	{ID: "claude-opus-4-5", DisplayName: "Claude Opus 4.5"},
	{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1"},
	{ID: "claude-sonnet-4-0", DisplayName: "Claude Sonnet 4"},
	{ID: "claude-opus-4-0", DisplayName: "Claude Opus 4"},
}

func defaultOAuthAllowedModels() []modelInfo {
	return append([]modelInfo(nil), oauthAllowedModels...)
}
