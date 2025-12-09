package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"anpr-service/internal/domain/anpr"
)

type ANPRRepository struct {
	db *gorm.DB
}

func NewANPRRepository(db *gorm.DB) *ANPRRepository {
	return &ANPRRepository{db: db}
}

func (Plate) TableName() string {
	return "anpr_plates"
}

func (ANPREvent) TableName() string {
	return "anpr_events"
}

func (List) TableName() string {
	return "anpr_lists"
}

func (ListItem) TableName() string {
	return "anpr_list_items"
}

func (EventPhoto) TableName() string {
	return "anpr_event_photos"
}

type Plate struct {
	ID         uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	Number     string    `gorm:"not null"`
	Normalized string    `gorm:"not null;uniqueIndex"`
	Country    *string
	Region     *string
	CreatedAt  time.Time
}

type ANPREvent struct {
	ID                uuid.UUID  `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	PlateID           *uuid.UUID `gorm:"type:uuid"`
	CameraID          string     `gorm:"not null"`
	CameraUUID        *uuid.UUID `gorm:"type:uuid"`
	PolygonID         *uuid.UUID `gorm:"type:uuid"`
	CameraModel       *string
	Direction         *string
	Lane              *int
	RawPlate          string `gorm:"not null"`
	NormalizedPlate   string `gorm:"not null"`
	Confidence        *float64
	VehicleColor      *string
	VehicleType       *string
	VehicleBrand      *string
	VehicleModel      *string
	VehicleCountry    *string
	VehiclePlateColor *string
	VehicleSpeed      *float64
	SnapshotURL       *string
	EventTime         time.Time      `gorm:"not null"`
	RawPayload        datatypes.JSON `gorm:"type:jsonb"`
	// Поля для данных о снеге
	SnowVolumePercentage *float64
	SnowVolumeConfidence *float64
	SnowVolumeM3         *float64
	MatchedSnow          bool `gorm:"default:false"`
	CreatedAt            time.Time
}

type List struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	Name        string    `gorm:"not null;uniqueIndex"`
	Type        string    `gorm:"not null"`
	Description *string
	CreatedAt   time.Time
}

type ListItem struct {
	ListID    uuid.UUID `gorm:"type:uuid;primaryKey"`
	PlateID   uuid.UUID `gorm:"type:uuid;primaryKey"`
	Note      *string
	CreatedAt time.Time
}

type VehicleData struct {
	Brand        string
	Model        string
	Color        string
	Year         int
	BodyVolumeM3 float64
}

type EventPhoto struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:uuid_generate_v4()"`
	EventID      uuid.UUID `gorm:"type:uuid;not null"`
	PhotoURL     string    `gorm:"not null"`
	DisplayOrder int       `gorm:"default:0"`
	CreatedAt    time.Time
}

func (r *ANPRRepository) GetOrCreatePlate(ctx context.Context, normalized, original string) (uuid.UUID, error) {
	var plate Plate
	err := r.db.WithContext(ctx).Where("normalized = ?", normalized).First(&plate).Error
	if err == nil {
		return plate.ID, nil
	}
	if err != gorm.ErrRecordNotFound {
		return uuid.Nil, err
	}

	plate = Plate{
		ID:         uuid.New(),
		Number:     original,
		Normalized: normalized,
		CreatedAt:  time.Now(),
	}
	if err := r.db.WithContext(ctx).Create(&plate).Error; err != nil {
		return uuid.Nil, fmt.Errorf("failed to create plate: %w", err)
	}
	return plate.ID, nil
}

func (r *ANPRRepository) CreateANPREvent(ctx context.Context, event *anpr.Event) error {
	dbEvent := ANPREvent{
		ID:              event.ID, // Use pre-generated ID
		PlateID:         &event.PlateID,
		CameraID:        event.CameraID,
		RawPlate:        event.Plate,
		NormalizedPlate: event.NormalizedPlate,
		EventTime:       event.EventTime,
		CreatedAt:       time.Now(),
	}

	if event.CameraModel != "" {
		dbEvent.CameraModel = &event.CameraModel
	}
	if event.Direction != "" {
		dbEvent.Direction = &event.Direction
	}
	if event.Lane != 0 {
		dbEvent.Lane = &event.Lane
	}
	if event.Confidence != 0 {
		dbEvent.Confidence = &event.Confidence
	}
	if event.Vehicle.Color != "" {
		dbEvent.VehicleColor = &event.Vehicle.Color
	}
	if event.Vehicle.Type != "" {
		dbEvent.VehicleType = &event.Vehicle.Type
	}
	if event.Vehicle.Brand != "" {
		dbEvent.VehicleBrand = &event.Vehicle.Brand
	}
	if event.Vehicle.Model != "" {
		dbEvent.VehicleModel = &event.Vehicle.Model
	}
	if event.Vehicle.Country != "" {
		dbEvent.VehicleCountry = &event.Vehicle.Country
	}
	if event.Vehicle.PlateColor != "" {
		dbEvent.VehiclePlateColor = &event.Vehicle.PlateColor
	}
	if event.Vehicle.Speed != nil {
		dbEvent.VehicleSpeed = event.Vehicle.Speed
	}
	if event.SnapshotURL != "" {
		dbEvent.SnapshotURL = &event.SnapshotURL
	}
	if len(event.RawPayload) > 0 {
		raw, err := json.Marshal(event.RawPayload)
		if err != nil {
			return fmt.Errorf("marshal raw payload: %w", err)
		}
		dbEvent.RawPayload = datatypes.JSON(raw)
	}

	// Сохраняем данные о снеге, если они есть
	if event.SnowVolumePercentage != nil {
		dbEvent.SnowVolumePercentage = event.SnowVolumePercentage
	}
	if event.SnowVolumeConfidence != nil {
		dbEvent.SnowVolumeConfidence = event.SnowVolumeConfidence
	}
	if event.SnowVolumeM3 != nil {
		dbEvent.SnowVolumeM3 = event.SnowVolumeM3
	}
	dbEvent.MatchedSnow = event.MatchedSnow

	if err := r.db.WithContext(ctx).Create(&dbEvent).Error; err != nil {
		return fmt.Errorf("failed to create ANPR event in database: %w", err)
	}

	event.ID = dbEvent.ID
	return nil
}

func (r *ANPRRepository) FindListsForPlate(ctx context.Context, plateID uuid.UUID) ([]anpr.ListHit, error) {
	var hits []anpr.ListHit

	err := r.db.WithContext(ctx).
		Table("anpr_list_items").
		Select("anpr_lists.id as list_id, anpr_lists.name as list_name, anpr_lists.type as list_type").
		Joins("JOIN anpr_lists ON anpr_list_items.list_id = anpr_lists.id").
		Where("anpr_list_items.plate_id = ?", plateID).
		Scan(&hits).Error

	if err != nil {
		return nil, err
	}

	return hits, nil
}

func (r *ANPRRepository) FindPlatesByNormalized(ctx context.Context, normalized string) ([]Plate, error) {
	var plates []Plate
	err := r.db.WithContext(ctx).
		Where("normalized = ?", normalized).
		Find(&plates).Error
	return plates, err
}

func (r *ANPRRepository) FindEvents(ctx context.Context, normalizedPlate *string, from, to *time.Time, direction *string, limit, offset int) ([]ANPREvent, error) {
	query := r.db.WithContext(ctx).Model(&ANPREvent{})

	if normalizedPlate != nil {
		query = query.Where("normalized_plate = ?", *normalizedPlate)
	}
	if from != nil {
		query = query.Where("event_time >= ?", *from)
	}
	if to != nil {
		query = query.Where("event_time <= ?", *to)
	}
	if direction != nil && *direction != "" {
		query = query.Where("direction = ?", *direction)
	}

	query = query.Order("event_time DESC")

	if limit > 0 {
		query = query.Limit(limit)
		if limit > 100 {
			query = query.Limit(100)
		}
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var events []ANPREvent
	err := query.Find(&events).Error
	return events, err
}

// FindEventsByPlateAndTime находит события по номеру, времени и направлению (для внутреннего использования)
func (r *ANPRRepository) FindEventsByPlateAndTime(ctx context.Context, normalizedPlate string, from, to time.Time, direction *string) ([]ANPREvent, error) {
	query := r.db.WithContext(ctx).Model(&ANPREvent{}).
		Where("normalized_plate = ?", normalizedPlate).
		Where("event_time >= ?", from).
		Where("event_time <= ?", to)

	if direction != nil && *direction != "" {
		query = query.Where("direction = ?", *direction)
	}

	query = query.Order("event_time ASC")

	var events []ANPREvent
	err := query.Find(&events).Error
	return events, err
}

func (r *ANPRRepository) GetLastEventTimeForPlate(ctx context.Context, plateID uuid.UUID) (*time.Time, error) {
	var event ANPREvent
	err := r.db.WithContext(ctx).
		Where("plate_id = ?", plateID).
		Order("event_time DESC").
		First(&event).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	return &event.EventTime, nil
}

// SyncVehicleToWhitelist синхронизирует номер из vehicles в whitelist
// Вызывается при создании/обновлении vehicle в roles сервисе
func (r *ANPRRepository) SyncVehicleToWhitelist(ctx context.Context, plateNumber string) (uuid.UUID, error) {
	var plateID uuid.UUID
	err := r.db.WithContext(ctx).
		Raw("SELECT anpr_sync_vehicle_to_whitelist(?)", plateNumber).
		Scan(&plateID).Error
	if err != nil {
		return uuid.Nil, fmt.Errorf("sync vehicle to whitelist: %w", err)
	}
	return plateID, nil
}

// GetVehicleByPlate получает данные о транспорте по нормализованному номеру
// Возвращает nil, если vehicle не найден или неактивен
func (r *ANPRRepository) GetVehicleByPlate(ctx context.Context, normalizedPlate string) (*VehicleData, error) {
	var vehicle struct {
		Brand        string
		Model        string
		Color        string
		Year         int
		BodyVolumeM3 float64
	}

	err := r.db.WithContext(ctx).
		Table("vehicles").
		Select("brand, model, color, year, body_volume_m3").
		Where("is_active = ? AND normalize_plate_number(plate_number) = ?", true, normalizedPlate).
		First(&vehicle).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil // Vehicle не найден - это нормально
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get vehicle: %w", err)
	}

	return &VehicleData{
		Brand:        vehicle.Brand,
		Model:        vehicle.Model,
		Color:        vehicle.Color,
		Year:         vehicle.Year,
		BodyVolumeM3: vehicle.BodyVolumeM3,
	}, nil
}

// CheckVehicleExists проверяет, существует ли номер в таблице vehicles
// Использует GetVehicleByPlate для проверки
func (r *ANPRRepository) CheckVehicleExists(ctx context.Context, normalizedPlate string) (bool, error) {
	vehicle, err := r.GetVehicleByPlate(ctx, normalizedPlate)
	if err != nil {
		return false, err
	}
	return vehicle != nil, nil
}

// DeleteOldEvents удаляет события старше указанного количества дней
func (r *ANPRRepository) DeleteOldEvents(ctx context.Context, days int) (int64, error) {
	cutoffTime := time.Now().AddDate(0, 0, -days)
	result := r.db.WithContext(ctx).
		Where("created_at < ?", cutoffTime).
		Delete(&ANPREvent{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// DeleteAllEvents удаляет все события из базы данных
func (r *ANPRRepository) DeleteAllEvents(ctx context.Context) (int64, error) {
	// Используем прямой SQL запрос для удаления всех событий
	// Фотографии удалятся автоматически благодаря ON DELETE CASCADE в таблице anpr_event_photos
	result := r.db.WithContext(ctx).Exec("DELETE FROM anpr_events")
	if result.Error != nil {
		return 0, fmt.Errorf("failed to delete events from database: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// CreateEventPhotos сохраняет фотографии события
func (r *ANPRRepository) CreateEventPhotos(ctx context.Context, eventID uuid.UUID, photoURLs []string) error {
	if len(photoURLs) == 0 {
		return nil
	}

	photos := make([]EventPhoto, 0, len(photoURLs))
	for i, url := range photoURLs {
		photos = append(photos, EventPhoto{
			EventID:      eventID,
			PhotoURL:     url,
			DisplayOrder: i,
			CreatedAt:    time.Now(),
		})
	}

	return r.db.WithContext(ctx).Create(&photos).Error
}

// GetEventByID получает событие по ID
func (r *ANPRRepository) GetEventByID(ctx context.Context, eventID uuid.UUID) (*ANPREvent, error) {
	var event ANPREvent
	err := r.db.WithContext(ctx).Where("id = ?", eventID).First(&event).Error
	if err == gorm.ErrRecordNotFound {
		return nil, nil // Событие не найдено
	}
	if err != nil {
		return nil, err
	}
	return &event, nil
}

// GetEventPhotos получает все фотографии события
func (r *ANPRRepository) GetEventPhotos(ctx context.Context, eventID uuid.UUID) ([]EventPhoto, error) {
	var photos []EventPhoto
	err := r.db.WithContext(ctx).
		Where("event_id = ?", eventID).
		Order("display_order ASC").
		Find(&photos).Error
	return photos, err
}
