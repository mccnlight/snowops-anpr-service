package repository

import "testing"

func TestDisplayOrderFromPhotoURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		fallback int
		want     int
	}{
		{
			name:     "extracts plain jpg index",
			url:      "https://example.com/event-photo-1.jpg",
			fallback: 0,
			want:     1,
		},
		{
			name:     "extracts index with query string",
			url:      "https://example.com/event-photo-3.png?sig=abc",
			fallback: 0,
			want:     3,
		},
		{
			name:     "falls back when suffix is absent",
			url:      "https://example.com/photo.jpg",
			fallback: 7,
			want:     7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayOrderFromPhotoURL(tt.url, tt.fallback)
			if got != tt.want {
				t.Fatalf("displayOrderFromPhotoURL() = %d, want %d", got, tt.want)
			}
		})
	}
}
