package decoder

import (
	"testing"
	"time"
)

func TestWithinTolerance(t *testing.T) {
	tests := []struct {
		name      string
		value     time.Duration
		reference time.Duration
		tolerance time.Duration
		expected  bool
	}{
		{"Exact Match", 100 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Within Upper Bound", 105 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Within Lower Bound", 95 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, true},
		{"Outside Upper Bound", 111 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
		{"Outside Lower Bound", 89 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
		{"Outside Lower Bound", 50 * time.Millisecond, 100 * time.Millisecond, 10 * time.Millisecond, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := withinTolerance(tt.value, tt.reference, tt.tolerance)
			if result != tt.expected {
				t.Errorf("withinTolerance(%v, %v, %v) = %v; expected %v",
					tt.value, tt.reference, tt.tolerance, result, tt.expected)
			}
		})
	}
}
