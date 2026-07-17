package agentclient

// RegisterDefaults is set by the cmd/agent init via RegisterAllAdapters callback
// to avoid this package importing claude/codex/grok (circular with tests).
// cmd/agent should call claude.Register(); codex.Register(); grok.Register() at startup.
