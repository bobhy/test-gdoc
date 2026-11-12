//go:build js && wasm

// Command plugin is the WebAssembly entrypoint loaded by the Obsidian plugin.
package main

import (
	"encoding/json"
	"sync"
	"syscall/js"

	"sample-go-plugin/go/settings"
)

const globalNamespace = "__gotestWasm"

type errorResponse struct {
	Error string `json:"error"`
}

func main() {
	done := make(chan struct{})
	var closeOnce sync.Once

	runActionFn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return marshalJSON(errorResponse{Error: "expected settings JSON string argument"})
		}

		var cfg settings.PluginSettings
		if err := json.Unmarshal([]byte(args[0].String()), &cfg); err != nil {
			return marshalJSON(errorResponse{Error: "invalid settings JSON: " + err.Error()})
		}

		return marshalJSON(settings.RunAction(cfg))
	})
	defer runActionFn.Release()

	var shutdownFn js.Func
	shutdownFn = js.FuncOf(func(_ js.Value, _ []js.Value) any {
		closeOnce.Do(func() {
			js.Global().Delete(globalNamespace)
			close(done)
		})
		return nil
	})
	defer shutdownFn.Release()

	bridge := js.Global().Get("Object").New()
	bridge.Set("runAction", runActionFn)
	bridge.Set("shutdown", shutdownFn)
	js.Global().Set(globalNamespace, bridge)

	<-done
}

func marshalJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return `{"error":"failed to marshal response"}`
	}
	return string(data)
}
