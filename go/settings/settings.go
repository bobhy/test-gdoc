// Package settings defines the plugin configuration shared between Go and TypeScript,
// along with the core Go action logic invoked from the Obsidian UI.
package settings

import (
	"fmt"
	"runtime"
	"strings"
)

// PluginSettings holds the Obsidian plugin settings shared with Go.
// Its JSON tags and default values are the single source of truth for the TypeScript UI.
type PluginSettings struct {
	GreetingPrefix string `json:"greetingPrefix"`
	RepeatCount    int    `json:"repeatCount"`
	Uppercase      bool   `json:"uppercase"`
}

// DefaultSettings returns the canonical default settings for the plugin.
func DefaultSettings() PluginSettings {
	return PluginSettings{
		GreetingPrefix: "Hello from Go Wasm",
		RepeatCount:    1,
		Uppercase:      false,
	}
}

// ActionResult is the structured response returned by RunAction to the Obsidian UI.
type ActionResult struct {
	Message   string `json:"message"`
	GoVersion string `json:"goVersion"`
	Platform  string `json:"platform"`
}

// RunAction executes the sample Go action routine using the provided plugin settings.
func RunAction(cfg PluginSettings) ActionResult {
	prefix := strings.TrimSpace(cfg.GreetingPrefix)
	if prefix == "" {
		prefix = DefaultSettings().GreetingPrefix
	}

	count := cfg.RepeatCount
	if count < 1 {
		count = 1
	} else if count > 10 {
		count = 10
	}

	parts := make([]string, count)
	for i := range parts {
		parts[i] = prefix
	}
	msg := strings.Join(parts, " | ")
	if cfg.Uppercase {
		msg = strings.ToUpper(msg)
	}

	return ActionResult{
		Message:   fmt.Sprintf("%s (%s)", msg, runtime.Version()),
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}
