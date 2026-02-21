package main

import (
	"os"
	"testing"
)

func TestExpandEnvOnFlagValues(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		envKey string
		envVal string
		want   string
	}{
		{
			name:   "braced var in URL",
			input:  "http://localhost:${PORT}/path",
			envKey: "PORT",
			envVal: "8080",
			want:   "http://localhost:8080/path",
		},
		{
			name:   "unbraced var in URL",
			input:  "http://localhost:$PORT/path",
			envKey: "PORT",
			envVal: "9090",
			want:   "http://localhost:9090/path",
		},
		{
			name:   "undefined var expands to empty",
			input:  "http://localhost:${UNDEFINED_VAR_12345}/path",
			envKey: "",
			envVal: "",
			want:   "http://localhost:/path",
		},
		{
			name:   "no vars passes through unchanged",
			input:  "http://localhost:3000/path",
			envKey: "",
			envVal: "",
			want:   "http://localhost:3000/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envKey != "" {
				t.Setenv(tt.envKey, tt.envVal)
			}
			got := os.ExpandEnv(tt.input)
			if got != tt.want {
				t.Errorf("os.ExpandEnv(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
