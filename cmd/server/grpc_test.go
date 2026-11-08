package main

import "testing"

func TestGRPCAddr(t *testing.T) {
	t.Parallel()

	tests := []struct{ addr, port, want string }{
		{"", "", ""},
		{"", "9090", ":9090"},
		{"", ":9090", ":9090"},
		{"", " 9090 ", ":9090"},
		{"127.0.0.1:7000", "9090", "127.0.0.1:7000"},
		{" ", " ", ""},
	}

	for _, tc := range tests {
		if got := grpcAddr(tc.addr, tc.port); got != tc.want {
			t.Errorf("grpcAddr(%q, %q) = %q, want %q", tc.addr, tc.port, got, tc.want)
		}
	}
}
