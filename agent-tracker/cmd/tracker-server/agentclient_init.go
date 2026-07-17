package main

import (
	"github.com/david/agent-tracker/internal/agentclient/claude"
	"github.com/david/agent-tracker/internal/agentclient/codex"
	"github.com/david/agent-tracker/internal/agentclient/grok"
)

// Register adapters so WatchHints() aggregation sees Claude (and any future
// sources). Grok returns empty hints (poll-only).
func init() {
	claude.Register()
	codex.Register()
	grok.Register()
}
