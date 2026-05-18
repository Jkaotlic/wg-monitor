package main

import "testing"

func TestShortCommandID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{
			name: "empty",
			id:   "",
			want: "",
		},
		{
			name: "short",
			id:   "abc123",
			want: "abc123",
		},
		{
			name: "exactly 12",
			id:   "abcdefghijkl",
			want: "abcdefghijkl",
		},
		{
			name: "long",
			id:   "abcdefghijklmnop",
			want: "abcdefghijkl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortCommandID(tt.id); got != tt.want {
				t.Fatalf("shortCommandID(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
