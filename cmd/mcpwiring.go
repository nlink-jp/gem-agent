package cmd

import (
	"strings"

	"github.com/nlink-jp/gem-agent/internal/config"
	"github.com/nlink-jp/gem-agent/internal/llm"
	"github.com/nlink-jp/gem-agent/internal/tools"
	"github.com/nlink-jp/gem-agent/internal/uitext"
)

// The small pieces that connect ADR-0083 to the rest of cmd: the
// agent's predicate and hook (nil unless the operator opted in), the
// system prompt's server list and trigger, and the librarian's answer
// language.

// advertisePredicate is Options.Advertise: nil under "all", so an
// operator who did not opt in has today's declarations exactly.
func advertisePredicate(cfg *config.Config, adv *mcpAdvertiser) func(name string) bool {
	if !cfg.MCP.OnRequest() {
		return nil
	}
	return adv.Advertise
}

// registeredIn is setInventory's registry predicate: the inventory also
// lists the names an exclusion removed, and those are not tools.
func registeredIn(registry *tools.Registry) func(name string) bool {
	return func(name string) bool { _, ok := registry.Get(name); return ok }
}

// afterToolHook is Options.AfterTool, on the same condition.
func afterToolHook(cfg *config.Config, adv *mcpAdvertiser) func(tc llm.ToolCall, abandoned bool) bool {
	if !cfg.MCP.OnRequest() {
		return nil
	}
	return adv.AfterTool
}

// mcpOnRequestTrigger is the sentence that makes the loading tools
// fire (ADR-0083 §5): the condition, the two tools, and nothing about
// any other path. Pinned by test.
const mcpOnRequestTrigger = "When a task needs something one of these servers does and no tool for it is in your tool list, call find_tools with the task in your own words; when you know which server, call mcp_load with its name. Loading runs nothing on a server."

// mcpOnRequestSection is the system prompt's server list under
// on-request: one line per connected server, then the trigger. Empty
// under "all" and when no server registered tools.
func mcpOnRequestSection(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return "\n\nMCP servers connected this session — their tools are not in your tool list until loaded:\n" +
		strings.Join(lines, "\n") + "\n" + mcpOnRequestTrigger + "\n"
}

// librarianLanguage names the language the librarian writes its
// reasons in: the session's (ADR-0083 §2).
func librarianLanguage(l uitext.Lang) string {
	if l == uitext.JA {
		return "Japanese"
	}
	return "English"
}
