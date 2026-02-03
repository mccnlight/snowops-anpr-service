package service

import (
	"testing"
)

func TestFormatVehicleInfo(t *testing.T) {
	tests := []struct {
		name     string
		brand    *string
		model    *string
		expected string
	}{
		{
			name:     "both brand and model",
			brand:    stringPtr("Toyota"),
			model:    stringPtr("Camry"),
			expected: "Toyota Camry",
		},
		{
			name:     "only brand",
			brand:    stringPtr("Toyota"),
			model:    nil,
			expected: "Toyota",
		},
		{
			name:     "only model",
			brand:    nil,
			model:    stringPtr("Camry"),
			expected: "Camry",
		},
		{
			name:     "both empty",
			brand:    nil,
			model:    nil,
			expected: "",
		},
		{
			name:     "brand with spaces",
			brand:    stringPtr("  Toyota  "),
			model:    stringPtr("  Camry  "),
			expected: "Toyota Camry",
		},
		{
			name:     "brand with double spaces",
			brand:    stringPtr("Toyota   Camry"),
			model:    stringPtr("Hybrid"),
			expected: "Toyota Camry Hybrid",
		},
		{
			name:     "empty strings",
			brand:    stringPtr(""),
			model:    stringPtr(""),
			expected: "",
		},
		{
			name:     "empty brand, valid model",
			brand:    stringPtr(""),
			model:    stringPtr("Camry"),
			expected: "Camry",
		},
		{
			name:     "valid brand, empty model",
			brand:    stringPtr("Toyota"),
			model:    stringPtr(""),
			expected: "Toyota",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatVehicleInfo(tt.brand, tt.model)
			if result != tt.expected {
				t.Errorf("formatVehicleInfo() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func stringPtr(s string) *string {
	return &s
}
