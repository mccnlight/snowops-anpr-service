package anpr

import (
	"time"

	"github.com/google/uuid"
)

type VehicleInfo struct {
	Color      string   `json:"color,omitempty"`
	Type       string   `json:"type,omitempty"`
	Brand      string   `json:"brand,omitempty"`
	Model      string   `json:"model,omitempty"`
	Country    string   `json:"country,omitempty"`
	PlateColor string   `json:"plate_color,omitempty"`
	Speed      *float64 `json:"speed,omitempty"`
}

type EventPayload struct {
	CameraID    string                 `json:"camera_id"`
	CameraModel string                 `json:"camera_model,omitempty"`
	Plate       string                 `json:"plate"`
	Confidence  float64                `json:"confidence"`
	Direction   string                 `json:"direction"`
	Lane        int                    `json:"lane"`
	EventTime   time.Time              `json:"event_time"`
	Vehicle     VehicleInfo            `json:"vehicle"`
	SnapshotURL string                 `json:"snapshot_url,omitempty"`
	RawPayload  map[string]interface{} `json:"raw_payload,omitempty"`
	// Поля для данных о снеге
	SnowVolumePercentage *float64 `json:"snow_volume_percentage,omitempty"`
	SnowVolumeConfidence *float64 `json:"snow_volume_confidence,omitempty"`
	SnowVolumeM3         *float64 `json:"snow_volume_m3,omitempty"`
	MatchedSnow          bool     `json:"matched_snow,omitempty"`
}

type Event struct {
	ID      uuid.UUID
	PlateID uuid.UUID
	EventPayload
	NormalizedPlate string
}

type ListHit struct {
	ListID   uuid.UUID `json:"list_id"`
	ListName string    `json:"list_name"`
	ListType string    `json:"list_type"`
}

type ProcessResult struct {
	EventID       uuid.UUID `json:"event_id"`
	PlateID       uuid.UUID `json:"plate_id"`
	Plate         string    `json:"plate"`
	VehicleExists bool      `json:"vehicle_exists"`   // true если номер найден в vehicles
	Hits          []ListHit `json:"hits,omitempty"`   // Оставляем для обратной совместимости, всегда пустой
	PhotoURLs     []string  `json:"photos,omitempty"` // URLs загруженных фотографий
}

type EventPhoto struct {
	ID           uuid.UUID `json:"id"`
	EventID      uuid.UUID `json:"event_id"`
	PhotoURL     string    `json:"photo_url"`
	DisplayOrder int       `json:"display_order"`
	CreatedAt    time.Time `json:"created_at"`
}
