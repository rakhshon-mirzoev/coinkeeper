package bot

import "testing"

func TestIsStart(t *testing.T) {
	tests := []struct {
		text string
		want bool
	}{
		{"/start", true},
		{"  /start  ", true},
		{"/start@my_bot", true},
		{"/start something", true},
		{"/started", false},
		{"кофе 350", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := isStart(tt.text); got != tt.want {
			t.Errorf("isStart(%q) = %v, want %v", tt.text, got, tt.want)
		}
	}
}
