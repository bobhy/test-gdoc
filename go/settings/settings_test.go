package settings

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultSettingsJSONRoundTrip(t *testing.T) {
	want := DefaultSettings()
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var got PluginSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestRunAction(t *testing.T) {
	tests := []struct {
		name     string
		cfg      PluginSettings
		wantText string
	}{
		{
			name:     "defaults",
			cfg:      DefaultSettings(),
			wantText: "Hello from Go Wasm",
		},
		{
			name: "repeated and uppercase",
			cfg: PluginSettings{
				GreetingPrefix: "ping",
				RepeatCount:    3,
				Uppercase:      true,
			},
			wantText: "PING | PING | PING",
		},
		{
			name: "empty prefix and out-of-range count",
			cfg: PluginSettings{
				GreetingPrefix: "   ",
				RepeatCount:    0,
			},
			wantText: "Hello from Go Wasm",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := RunAction(tc.cfg)
			if !strings.HasPrefix(res.Message, tc.wantText) {
				t.Errorf("Message = %q, want prefix %q", res.Message, tc.wantText)
			}
			if res.GoVersion == "" {
				t.Error("GoVersion should not be empty")
			}
			if res.Platform == "" {
				t.Error("Platform should not be empty")
			}
		})
	}
}
