package claude

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/david/agent-tracker/internal/agentclient"
)

// Provider detection: the Claude process env's ANTHROPIC_BASE_URL mapped to a
// provider name via ~/.hat-env/providers/*.env (HAT-574 单一真身). The window's
// @agent_provider option is only a creation-time cache and goes stale on
// provider switches — this live probe is the single authoritative source.

// providersRelDir is the provider .env directory relative to home.
const providersRelDir = ".hat-env/providers"

// loadProviderMap reads providers/*.env into a map from the provider's
// ANTHROPIC_BASE_URL to its name (file basename). official unsets the URL and
// so is absent (treated as the empty-URL default).
func loadProviderMap(home string) map[string]string {
	m := map[string]string{}
	dir := filepath.Join(home, providersRelDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return m
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".env") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "unset") || !strings.Contains(line, "ANTHROPIC_BASE_URL=") {
				continue
			}
			v := line[strings.Index(line, "ANTHROPIC_BASE_URL=")+len("ANTHROPIC_BASE_URL="):]
			if v = strings.Trim(strings.TrimSpace(v), `"'`); v != "" {
				m[v] = strings.TrimSuffix(e.Name(), ".env")
			}
			break
		}
	}
	return m
}

// providerMap caches the providers/*.env load for one sync pass.
func (a *Adapter) providerMap(idx *agentclient.Index) map[string]string {
	v := idx.Memo("claude.providers", func() any { return loadProviderMap(a.home()) })
	m, _ := v.(map[string]string)
	return m
}

// providerForPID reads the process env's ANTHROPIC_BASE_URL (ps eww) and maps
// it to a provider name. Empty/unset → "anthropic" (the official login
// provider, which unsets the URL); a URL not in the map → "".
func (a *Adapter) providerForPID(idx *agentclient.Index, pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := agentclient.RunOutput(3*time.Second, "ps", "eww", "-p", strconv.Itoa(pid))
	if err != nil {
		return ""
	}
	return providerFromPSEnv(string(out), a.providerMap(idx))
}

// providerFromPSEnv resolves the provider from a `ps eww` output line (pure,
// unit-tested).
func providerFromPSEnv(psOut string, providers map[string]string) string {
	for _, tok := range strings.Fields(psOut) {
		if strings.HasPrefix(tok, "ANTHROPIC_BASE_URL=") {
			url := strings.TrimPrefix(tok, "ANTHROPIC_BASE_URL=")
			if name, ok := providers[url]; ok {
				return name
			}
			return ""
		}
	}
	return "anthropic"
}

// modelFromArgs reads the raw model name from the process command line
// (--model <value> / --model=<value>).
func modelFromArgs(command string) string {
	args := strings.Fields(command)
	for i, arg := range args {
		if arg == "--model" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(arg, "--model=") {
			return strings.TrimPrefix(arg, "--model=")
		}
	}
	return ""
}

// modelFromProviderEnv reads ANTHROPIC_MODEL from a provider .env file. Used
// for providers (e.g. minimax) that set the model via env var, not --model.
func (a *Adapter) modelFromProviderEnv(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(a.home(), providersRelDir, provider+".env"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		for _, prefix := range []string{"export ANTHROPIC_MODEL=", "ANTHROPIC_MODEL="} {
			if after, ok := strings.CutPrefix(line, prefix); ok {
				return strings.Trim(strings.TrimSpace(after), "\"'")
			}
		}
	}
	return ""
}
