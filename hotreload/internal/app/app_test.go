package app

import (
	"testing"
	"time"
)

func TestBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, baseBackoff}, // defensive: treated as the first attempt
		{1, baseBackoff},
		{2, 2 * baseBackoff},
		{3, 4 * baseBackoff},
		{4, 8 * baseBackoff},
		{6, maxBackoff}, // 32s would exceed the cap
		{50, maxBackoff},
	}

	for _, tt := range tests {
		if got := backoff(tt.attempt); got != tt.want {
			t.Errorf("backoff(%d) = %v, want %v", tt.attempt, got, tt.want)
		}
	}
}

func TestBackoffNeverExceedsCap(t *testing.T) {
	for n := 1; n <= 20; n++ {
		if got := backoff(n); got > maxBackoff {
			t.Fatalf("backoff(%d) = %v, above the %v cap", n, got, maxBackoff)
		}
	}
}
