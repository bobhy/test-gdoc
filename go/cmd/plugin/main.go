//go:build js && wasm

// Command plugin is the WebAssembly entrypoint loaded by the Obsidian plugin.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"syscall/js"

	"test-gdoc/go/gdrive"
)

const globalNamespace = "__testgdoc"

func main() {
	done := make(chan struct{})
	var closeOnce sync.Once

	service := NewBridgeService(jsRequestUrlDo, gdrive.DefaultRetryOptions())

	invokeFn := js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 2 || args[0].Type() != js.TypeString || args[1].Type() != js.TypeString {
			return rejectedJSPromise(errors.New("expected (method: string, payloadJson: string) arguments"))
		}
		method := args[0].String()
		payload := args[1].String()

		return newJSPromise(func() (any, error) {
			return service.Handle(context.Background(), method, payload)
		})
	})
	defer invokeFn.Release()

	shutdownFn := js.FuncOf(func(_ js.Value, _ []js.Value) any {
		closeOnce.Do(func() {
			js.Global().Delete(globalNamespace)
			close(done)
		})
		return nil
	})
	defer shutdownFn.Release()

	bridge := js.Global().Get("Object").New()
	bridge.Set("invoke", invokeFn)
	bridge.Set("shutdown", shutdownFn)
	js.Global().Set(globalNamespace, bridge)

	<-done
}

func newJSPromise(fn func() (any, error)) js.Value {
	var ctorFn js.Func
	ctorFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		ctorFn.Release()
		resolve := args[0]
		reject := args[1]
		go func() {
			res, err := fn()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			data, marshalErr := json.Marshal(res)
			if marshalErr != nil {
				reject.Invoke(js.Global().Get("Error").New("failed to marshal Go response: " + marshalErr.Error()))
				return
			}
			resolve.Invoke(string(data))
		}()
		return nil
	})
	return js.Global().Get("Promise").New(ctorFn)
}

func rejectedJSPromise(err error) js.Value {
	return js.Global().Get("Promise").Call("reject", js.Global().Get("Error").New(err.Error()))
}
