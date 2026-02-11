package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"anpr-service/internal/domain/anpr"
)

type ANPRRepository struct {
	db *gorm.DB
}

var cameraAliasToPolygonName = map[string]string{
	"shahovskoye": "Шаховское",
	"solnechniy":  "Солнечный",
	"solnechny":   "Солнечный",
	"solnechnyy":  "Солнечный",
	"yakor":       "Якорь",
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
	ContractorID      *uuid.UUID `gorm:"type:uuid"` // ID организации (подрядчика), к которой принадлежит машина
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
	ContractorID *uuid.UUID // ID организации (подрядчика), к которой принадлежит машина
}

type DriverData struct {
	ID       uuid.UUID
	FullName string
	IIN      string
	Phone    string
}

type ContractorData struct {
	ID   uuid.UUID
	Name string
	BIN  string
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

func (r *ANPRRepository) CreateANPREvent(
	ctx context.Context,
	event *anpr.Event,
	contractorID *uuid.UUID,
	polygonID *uuid.UUID,
) error {
	dbEvent := ANPREvent{
		ID:              event.ID, // Use pre-generated ID
		PlateID:         &event.PlateID,
		CameraID:        event.CameraID,
		PolygonID:       polygonID,
		RawPlate:        event.Plate,
		NormalizedPlate: event.NormalizedPlate,
		EventTime:       event.EventTime,
		ContractorID:    contractorID, // Сохраняем ID подрядчика напрямую в событии
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

func (r *ANPRRepository) ResolvePolygonIDByCameraID(ctx context.Context, cameraID string) (*uuid.UUID, error) {
	alias := strings.ToLower(strings.TrimSpace(cameraID))
	if alias == "" {
		return nil, nil
	}

	polygonName, ok := cameraAliasToPolygonName[alias]
	if !ok {
		return nil, nil
	}

	var polygon struct {
		ID uuid.UUID `gorm:"column:id"`
	}

	err := r.db.WithContext(ctx).
		Table("polygons").
		Select("id").
		Where("LOWER(name) = LOWER(?)", polygonName).
		Limit(1).
		First(&polygon).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("resolve polygon for camera_id %q: %w", cameraID, err)
	}

	polygonID := polygon.ID
	return &polygonID, nil
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
		ContractorID *uuid.UUID
	}

	err := r.db.WithContext(ctx).
		Table("vehicles").
		Select("brand, model, color, year, body_volume_m3, contractor_id").
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
		ContractorID: vehicle.ContractorID,
	}, nil
}

// GetDriverByVehiclePlate получает данные о водителе по номеру транспортного средства
// Возвращает nil, если водитель не найден или неактивен
func (r *ANPRRepository) GetDriverByVehiclePlate(ctx context.Context, normalizedPlate string) (*DriverData, error) {
	var driver struct {
		ID       uuid.UUID
		FullName string
		IIN      string
		Phone    string
	}

	err := r.db.WithContext(ctx).
		Table("vehicles").
		Select("drivers.id, drivers.full_name, drivers.iin, drivers.phone").
		Joins("INNER JOIN drivers ON vehicles.driver_id = drivers.id").
		Where("vehicles.is_active = ? AND drivers.is_active = ? AND normalize_plate_number(vehicles.plate_number) = ?",
			true, true, normalizedPlate).
		First(&driver).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil // Водитель не найден - это нормально
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get driver: %w", err)
	}

	return &DriverData{
		ID:       driver.ID,
		FullName: driver.FullName,
		IIN:      driver.IIN,
		Phone:    driver.Phone,
	}, nil
}

// GetContractorByVehiclePlate получает данные о подрядчике по номеру транспортного средства
// Возвращает nil, если подрядчик не найден или неактивен
func (r *ANPRRepository) GetContractorByVehiclePlate(ctx context.Context, normalizedPlate string) (*ContractorData, error) {
	var contractor struct {
		ID   uuid.UUID
		Name string
		BIN  string
	}

	err := r.db.WithContext(ctx).
		Table("vehicles").
		Select("organizations.id, organizations.name, organizations.bin").
		Joins("INNER JOIN organizations ON vehicles.contractor_id = organizations.id").
		Where("vehicles.is_active = ? AND organizations.is_active = ? AND normalize_plate_number(vehicles.plate_number) = ?",
			true, true, normalizedPlate).
		First(&contractor).Error

	if err == gorm.ErrRecordNotFound {
		return nil, nil // Подрядчик не найден - это нормально
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get contractor: %w", err)
	}

	return &ContractorData{
		ID:   contractor.ID,
		Name: contractor.Name,
		BIN:  contractor.BIN,
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

// ExistsRecentEvent проверяет, есть ли событие с тем же номером и камерой в окне +/- window
func (r *ANPRRepository) ExistsRecentEvent(ctx context.Context, normalizedPlate, cameraID string, eventTime time.Time, window time.Duration) (bool, error) {
	var count int64
	start := eventTime.Add(-window)
	end := eventTime.Add(window)
	err := r.db.WithContext(ctx).
		Model(&ANPREvent{}).
		Where("normalized_plate = ? AND camera_id = ? AND event_time BETWEEN ? AND ?", normalizedPlate, cameraID, start, end).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
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

// ReportEvent представляет событие для отчетов с данными о транспорте и подрядчике
type ReportEvent struct {
	ANPREvent
	VehicleID      *uuid.UUID `gorm:"column:vehicle_id"`
	ContractorID   *uuid.UUID `gorm:"column:contractor_id"`
	ContractorName *string    `gorm:"column:contractor_name"`
	VehicleBrand   *string
	VehicleModel   *string
	PlatePhotoURL  *string `gorm:"column:plate_photo_url"`
	BodyPhotoURL   *string `gorm:"column:body_photo_url"`
}

// GetReportEvents получает события для отчетов с фильтрацией
func (r *ANPRRepository) GetReportEvents(ctx context.Context, filters ReportFilters) ([]ReportEvent, error) {
	query := r.db.WithContext(ctx).
		Table("anpr_events AS e").
		Select(`
			e.*,
			v.id AS vehicle_id,
			COALESCE(e.contractor_id, v.contractor_id) AS contractor_id,
			o.name AS contractor_name,
			v.brand AS vehicle_brand,
			v.model AS vehicle_model,
			(SELECT photo_url FROM anpr_event_photos WHERE event_id = e.id AND display_order = 0 LIMIT 1) AS plate_photo_url,
			(SELECT photo_url FROM anpr_event_photos WHERE event_id = e.id AND display_order = 1 LIMIT 1) AS body_photo_url
		`).
		Joins("LEFT JOIN vehicles v ON normalize_plate_number(v.plate_number) = e.normalized_plate AND v.is_active = true").
		Joins("LEFT JOIN organizations o ON o.id = COALESCE(e.contractor_id, v.contractor_id)").
		Where("e.snow_volume_m3 IS NOT NULL AND e.snow_volume_m3 > 0") // Только события с объемом

	// Фильтр по подрядчику (если указан)
	// Используем поле contractor_id из anpr_events (если есть), иначе через JOIN с vehicles
	if filters.ContractorID != nil {
		query = query.Where("(e.contractor_id = ? OR v.contractor_id = ?)", *filters.ContractorID, *filters.ContractorID)
	}

	// Фильтр по полигону
	if filters.PolygonID != nil {
		query = query.Where("e.polygon_id = ?", *filters.PolygonID)
	}

	// Фильтр по периоду
	if !filters.From.IsZero() {
		query = query.Where("e.event_time >= ?", filters.From)
	}
	if !filters.To.IsZero() {
		query = query.Where("e.event_time <= ?", filters.To)
	}

	// Фильтр по номеру (поиск)
	if filters.PlateNumber != nil && *filters.PlateNumber != "" {
		normalized := fmt.Sprintf("%%%s%%", *filters.PlateNumber)
		query = query.Where("e.normalized_plate LIKE ? OR e.raw_plate LIKE ?", normalized, normalized)
	}

	// Фильтр по vehicle_id
	if filters.VehicleID != nil {
		query = query.Where("v.id = ?", *filters.VehicleID)
	}

	// Для подрядчиков показываем только привязанные события
	if filters.OnlyAssigned {
		query = query.Where("(e.contractor_id IS NOT NULL OR v.contractor_id IS NOT NULL)")
	}

	query = query.Order("e.event_time DESC")

	if filters.Limit > 0 {
		query = query.Limit(filters.Limit)
	}
	if filters.Offset > 0 {
		query = query.Offset(filters.Offset)
	}

	var events []ReportEvent
	err := query.Scan(&events).Error
	return events, err
}

// GetReportStats получает статистику для отчетов (сумма объема, количество поездок)
func (r *ANPRRepository) GetReportStats(ctx context.Context, filters ReportFilters) (*ReportStats, error) {
	query := r.db.WithContext(ctx).
		Table("anpr_events AS e").
		Select(`
			COALESCE(SUM(e.snow_volume_m3), 0) AS total_volume,
			COUNT(*) AS trip_count
		`).
		Joins("LEFT JOIN vehicles v ON normalize_plate_number(v.plate_number) = e.normalized_plate AND v.is_active = true").
		Where("e.snow_volume_m3 IS NOT NULL AND e.snow_volume_m3 > 0")

	// Применяем те же фильтры, что и в GetReportEvents
	// Используем поле contractor_id из anpr_events (если есть), иначе через JOIN с vehicles
	if filters.ContractorID != nil {
		query = query.Where("(e.contractor_id = ? OR v.contractor_id = ?)", *filters.ContractorID, *filters.ContractorID)
	}
	if filters.PolygonID != nil {
		query = query.Where("e.polygon_id = ?", *filters.PolygonID)
	}
	if !filters.From.IsZero() {
		query = query.Where("e.event_time >= ?", filters.From)
	}
	if !filters.To.IsZero() {
		query = query.Where("e.event_time <= ?", filters.To)
	}
	if filters.PlateNumber != nil && *filters.PlateNumber != "" {
		normalized := fmt.Sprintf("%%%s%%", *filters.PlateNumber)
		query = query.Where("e.normalized_plate LIKE ? OR e.raw_plate LIKE ?", normalized, normalized)
	}
	if filters.VehicleID != nil {
		query = query.Where("v.id = ?", *filters.VehicleID)
	}
	if filters.OnlyAssigned {
		query = query.Where("(e.contractor_id IS NOT NULL OR v.contractor_id IS NOT NULL)")
	}

	var stats ReportStats
	err := query.Scan(&stats).Error
	return &stats, err
}

// ReportFilters содержит фильтры для отчетов
type ReportFilters struct {
	ContractorID *uuid.UUID
	PolygonID    *uuid.UUID
	VehicleID    *uuid.UUID
	PlateNumber  *string
	From         time.Time
	To           time.Time
	OnlyAssigned bool // Только привязанные события (для подрядчиков)
	Limit        int
	Offset       int
	MaxRows      int // Максимальное количество строк для экспорта
}

// ReportStats содержит статистику для отчетов
type ReportStats struct {
	TotalVolume float64 `gorm:"column:total_volume"`
	TripCount   int64   `gorm:"column:trip_count"`
}

// WeekdayReportStats содержит агрегированные показатели по дням недели (ISO: 1=Пн ... 7=Вс)
type WeekdayReportStats struct {
	ISOWeekday  int     `gorm:"column:iso_weekday"`
	TotalVolume float64 `gorm:"column:total_volume"`
	TripCount   int64   `gorm:"column:trip_count"`
}

// GetReportWeekdayStats получает агрегированную статистику по дням недели.
// Данные агрегируются по всем неделям выбранного периода.
func (r *ANPRRepository) GetReportWeekdayStats(ctx context.Context, filters ReportFilters) ([]WeekdayReportStats, error) {
	query := r.db.WithContext(ctx).
		Table("anpr_events AS e").
		Select(`
			EXTRACT(
				ISODOW
				FROM ((e.event_time AT TIME ZONE 'Asia/Qyzylorda') - INTERVAL '16 hours')
			)::int AS iso_weekday,
			COALESCE(SUM(e.snow_volume_m3), 0) AS total_volume,
			COUNT(*) AS trip_count
		`).
		Joins("LEFT JOIN vehicles v ON normalize_plate_number(v.plate_number) = e.normalized_plate AND v.is_active = true").
		Where("e.snow_volume_m3 IS NOT NULL AND e.snow_volume_m3 > 0").
		Where("((e.event_time AT TIME ZONE 'Asia/Qyzylorda')::time >= TIME '16:00:00' OR (e.event_time AT TIME ZONE 'Asia/Qyzylorda')::time < TIME '10:00:00')")

	if filters.ContractorID != nil {
		query = query.Where("(e.contractor_id = ? OR v.contractor_id = ?)", *filters.ContractorID, *filters.ContractorID)
	}
	if filters.PolygonID != nil {
		query = query.Where("e.polygon_id = ?", *filters.PolygonID)
	}
	if !filters.From.IsZero() {
		query = query.Where("e.event_time >= ?", filters.From)
	}
	if !filters.To.IsZero() {
		query = query.Where("e.event_time <= ?", filters.To)
	}
	if filters.PlateNumber != nil && *filters.PlateNumber != "" {
		normalized := fmt.Sprintf("%%%s%%", *filters.PlateNumber)
		query = query.Where("e.normalized_plate LIKE ? OR e.raw_plate LIKE ?", normalized, normalized)
	}
	if filters.VehicleID != nil {
		query = query.Where("v.id = ?", *filters.VehicleID)
	}
	if filters.OnlyAssigned {
		query = query.Where("(e.contractor_id IS NOT NULL OR v.contractor_id IS NOT NULL)")
	}

	query = query.Group("iso_weekday").Order("iso_weekday ASC")

	var rows []WeekdayReportStats
	err := query.Scan(&rows).Error
	return rows, err
}

// GetReportEventsForExcel получает события для Excel выгрузки порциями с правильной сортировкой
// Сортировка: contractor_id ASC NULLS LAST, normalized_plate ASC, event_time DESC
// Работает без таблиц vehicles и organizations (использует только данные из anpr_events)
func (r *ANPRRepository) GetReportEventsForExcel(ctx context.Context, filters ReportFilters, pageSize, offset int) ([]ReportEvent, error) {
	query := r.db.WithContext(ctx).
		Table("anpr_events AS e").
		Select(`
			e.*,
			NULL::UUID AS vehicle_id,
			e.contractor_id AS contractor_id,
			o.name AS contractor_name,
			e.vehicle_brand AS vehicle_brand,
			e.vehicle_model AS vehicle_model,
			(SELECT photo_url FROM anpr_event_photos WHERE event_id = e.id AND display_order = 0 LIMIT 1) AS plate_photo_url,
			(SELECT photo_url FROM anpr_event_photos WHERE event_id = e.id AND display_order = 1 LIMIT 1) AS body_photo_url
		`).
		Joins("LEFT JOIN organizations o ON o.id = e.contractor_id")
	// Для Excel выгрузки показываем все события, не только с snow_volume_m3 > 0

	// Фильтр по подрядчику (если указан)
	if filters.ContractorID != nil {
		query = query.Where("e.contractor_id = ?", *filters.ContractorID)
	}

	// Фильтр по полигону
	if filters.PolygonID != nil {
		query = query.Where("e.polygon_id = ?", *filters.PolygonID)
	}

	// Фильтр по периоду
	if !filters.From.IsZero() {
		query = query.Where("e.event_time >= ?", filters.From)
	}
	if !filters.To.IsZero() {
		query = query.Where("e.event_time <= ?", filters.To)
	}

	// Фильтр по номеру (поиск)
	if filters.PlateNumber != nil && *filters.PlateNumber != "" {
		normalized := fmt.Sprintf("%%%s%%", *filters.PlateNumber)
		query = query.Where("e.normalized_plate LIKE ? OR e.raw_plate LIKE ?", normalized, normalized)
	}

	// Фильтр по vehicle_id - пропускаем, так как нет таблицы vehicles
	// if filters.VehicleID != nil {
	//     query = query.Where("v.id = ?", *filters.VehicleID)
	// }

	// Для подрядчиков показываем только привязанные события
	if filters.OnlyAssigned {
		query = query.Where("e.contractor_id IS NOT NULL")
	}

	// Сортировка для группировки: contractor_name (NULLS LAST), plate, event_time
	query = query.Order("o.name ASC NULLS LAST, e.normalized_plate ASC, e.event_time DESC")

	// Пагинация
	if pageSize > 0 {
		query = query.Limit(pageSize)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}

	var events []ReportEvent
	err := query.Scan(&events).Error
	return events, err
}

// CountReportEventsForExcel подсчитывает общее количество событий для Excel выгрузки
// Работает без таблиц vehicles и organizations (использует только данные из anpr_events)
func (r *ANPRRepository) CountReportEventsForExcel(ctx context.Context, filters ReportFilters) (int64, error) {
	query := r.db.WithContext(ctx).
		Table("anpr_events AS e")
		// Для Excel выгрузки показываем все события, не только с snow_volume_m3 > 0

	// Применяем те же фильтры, что и в GetReportEventsForExcel
	if filters.ContractorID != nil {
		query = query.Where("e.contractor_id = ?", *filters.ContractorID)
	}
	if filters.PolygonID != nil {
		query = query.Where("e.polygon_id = ?", *filters.PolygonID)
	}
	if !filters.From.IsZero() {
		query = query.Where("e.event_time >= ?", filters.From)
	}
	if !filters.To.IsZero() {
		query = query.Where("e.event_time <= ?", filters.To)
	}
	if filters.PlateNumber != nil && *filters.PlateNumber != "" {
		normalized := fmt.Sprintf("%%%s%%", *filters.PlateNumber)
		query = query.Where("e.normalized_plate LIKE ? OR e.raw_plate LIKE ?", normalized, normalized)
	}
	// Фильтр по vehicle_id - пропускаем, так как нет таблицы vehicles
	// if filters.VehicleID != nil {
	//     query = query.Where("v.id = ?", *filters.VehicleID)
	// }
	if filters.OnlyAssigned {
		query = query.Where("e.contractor_id IS NOT NULL")
	}

	var count int64
	err := query.Count(&count).Error
	return count, err
}
