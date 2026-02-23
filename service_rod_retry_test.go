package main

import (
	"errors"
	"testing"
)

func TestIsRodSessionNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cdp -32001", err: errors.New("panic: {-32001 Session with given id not found. }"), want: true},
		{name: "session string", err: errors.New("Session with given id not found"), want: true},
		{name: "other", err: errors.New("context deadline exceeded"), want: false},
	}
	for _, tt := range tests {
		if got := isRodSessionNotFound(tt.err); got != tt.want {
			t.Fatalf("%s: got %v want %v", tt.name, got, tt.want)
		}
	}
}
