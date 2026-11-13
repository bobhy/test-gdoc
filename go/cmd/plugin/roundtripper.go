//go:build js && wasm

package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"

	"test-gdoc/go/gdrive"
)

const hostNamespace = "__testgdocHost"

// jsRequestUrlDo executes HTTP requests via the TypeScript Obsidian requestUrl() callback
// mounted on window.__testgdocHost.httpFetch.
func jsRequestUrlDo(ctx context.Context, req *gdrive.HTTPRequest) (*gdrive.HTTPResponse, error) {
	host := js.Global().Get(hostNamespace)
	if host.IsUndefined() || host.IsNull() {
		return nil, errors.New("test-gdoc host bridge is not initialized")
	}
	fetchFn := host.Get("httpFetch")
	if fetchFn.Type() != js.TypeFunction {
		return nil, errors.New("test-gdoc host httpFetch callback is missing")
	}

	payload := js.Global().Get("Object").New()
	payload.Set("method", req.Method)
	payload.Set("url", req.URL)

	hdrObj := js.Global().Get("Object").New()
	for k, v := range req.Headers {
		hdrObj.Set(k, v)
	}
	payload.Set("headers", hdrObj)

	if len(req.Body) > 0 {
		u8 := js.Global().Get("Uint8Array").New(len(req.Body))
		js.CopyBytesToJS(u8, req.Body)
		payload.Set("body", u8)
	}

	resCh := make(chan *gdrive.HTTPResponse, 1)
	errCh := make(chan error, 1)

	var resolveFn, rejectFn js.Func
	var releaseOnce sync.Once
	releaseBoth := func() {
		releaseOnce.Do(func() {
			resolveFn.Release()
			rejectFn.Release()
		})
	}

	resolveFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer releaseBoth()
		if len(args) == 0 {
			errCh <- errors.New("httpFetch resolved with empty response")
			return nil
		}
		resVal := args[0]
		status := resVal.Get("status").Int()

		respHeaders := map[string]string{}
		hdrVal := resVal.Get("headers")
		if !hdrVal.IsUndefined() && !hdrVal.IsNull() {
			keys := js.Global().Get("Object").Call("keys", hdrVal)
			n := keys.Length()
			for i := range n {
				k := keys.Index(i).String()
				respHeaders[k] = hdrVal.Get(k).String()
			}
		}

		var bodyBytes []byte
		bodyVal := resVal.Get("body")
		if !bodyVal.IsUndefined() && !bodyVal.IsNull() {
			byteLen := bodyVal.Get("byteLength").Int()
			if byteLen > 0 {
				bodyBytes = make([]byte, byteLen)
				js.CopyBytesToGo(bodyBytes, bodyVal)
			}
		}

		resCh <- &gdrive.HTTPResponse{
			StatusCode: status,
			Headers:    respHeaders,
			Body:       bodyBytes,
		}
		return nil
	})

	rejectFn = js.FuncOf(func(_ js.Value, args []js.Value) any {
		defer releaseBoth()
		msg := "requestUrl failed"
		if len(args) > 0 && !args[0].IsUndefined() && !args[0].IsNull() {
			msgVal := args[0].Get("message")
			if !msgVal.IsUndefined() {
				msg = msgVal.String()
			} else {
				msg = fmt.Sprint(args[0])
			}
		}
		errCh <- errors.New(msg)
		return nil
	})

	promise := fetchFn.Invoke(payload)
	promise.Call("then", resolveFn, rejectFn)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case resp := <-resCh:
		return resp, nil
	}
}
