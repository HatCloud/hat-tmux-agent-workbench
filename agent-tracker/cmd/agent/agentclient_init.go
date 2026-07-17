package main

import (
	"github.com/david/agent-tracker/internal/agentclient/claude"
	"github.com/david/agent-tracker/internal/agentclient/codex"
	"github.com/david/agent-tracker/internal/agentclient/grok"
)

func init() {
	claude.Register()
	codex.Register()
	grok.Register()
}
