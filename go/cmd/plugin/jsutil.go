//go:build js && wasm

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"
)

const hostNamespace = "__testgdocHost"

type hostHandles struct {
	host           js.Value
	app            js.Value
	obsidian       js.Value
	requestUrlFn   js.Value
	noticeCtor     js.Value
	uint8ArrayCtor js.Value
	objectCtor     js.Value
	jsonObj        js.Value
}

var (
	hostMu     sync.Mutex
	cachedHost *hostHandles
)

func resetHostCache() {
	hostMu.Lock()
	cachedHost = nil
	hostMu.Unlock()
}

func getHostHandles() (*hostHandles, error) {
	hostMu.Lock()
	defer hostMu.Unlock()

	host := js.Global().Get(hostNamespace)
	if host.IsUndefined() || host.IsNull() {
		cachedHost = nil
		return nil, errors.New("test-gdoc host bridge is not initialized")
	}
	if cachedHost != nil && cachedHost.host.Equal(host) {
		return cachedHost, nil
	}

	app := host.Get("app")
	obs := host.Get("obsidian")
	if obs.IsUndefined() || obs.IsNull() {
		return nil, errors.New("test-gdoc host obsidian module is missing")
	}
	reqFn := obs.Get("requestUrl")
	if reqFn.Type() != js.TypeFunction {
		return nil, errors.New("obsidian.requestUrl is missing on host bridge")
	}

	cachedHost = &hostHandles{
		host:           host,
		app:            app,
		obsidian:       obs,
		requestUrlFn:   reqFn,
		noticeCtor:     obs.Get("Notice"),
		uint8ArrayCtor: js.Global().Get("Uint8Array"),
		objectCtor:     js.Global().Get("Object"),
		jsonObj:        js.Global().Get("JSON"),
	}
	return cachedHost, nil
}

// awaitPromise parks the calling goroutine until the JS Promise settles or ctx is canceled.
// Releases both js.Func callbacks once via sync.Once to prevent _values table leaks.
func awaitPromise(ctx context.Context, promise js.Value) (js.Value, error) {
	if promise.IsUndefined() || promise.IsNull() {
		return js.Undefined(), errors.New("awaitPromise: expected Promise, got null/undefined")
	}

	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)

	var resolveFn, rejectFn js.Func
	var once sync.Once
	release := func() {
		once.Do(func() {
			resolveFn.Release()
			rejectFn.Release()
		})
	}

	resolveFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		if len(args) > 0 {
			resCh <- args[0]
		} else {
			resCh <- js.Undefined()
		}
		return nil
	})

	rejectFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer release()
		msg := "JavaScript promise rejected"
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			if m := args[0].Get("message"); !m.IsUndefined() {
				msg = m.String()
			} else {
				msg = fmt.Sprint(args[0])
			}
		}
		errCh <- errors.New(msg)
		return nil
	})

	promise.Call("then", resolveFn, rejectFn)

	select {
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	case err := <-errCh:
		return js.Undefined(), err
	case val := <-resCh:
		return val, nil
	}
}
