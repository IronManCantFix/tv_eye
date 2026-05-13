package task

import "testing"

func TestIsHEVCCodec(t *testing.T) {
	tests := []struct {
		codec string
		want  bool
	}{
		{codec: "hevc", want: true},
		{codec: "h265", want: true},
		{codec: "h.265", want: true},
		{codec: "H265", want: true},
		{codec: "h264", want: false},
		{codec: "avc1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.codec, func(t *testing.T) {
			if got := isHEVCCodec(tt.codec); got != tt.want {
				t.Fatalf("expected %t, got %t", tt.want, got)
			}
		})
	}
}
