package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/xuri/excelize/v2"

	"anpr-service/internal/domain/anpr"
	"anpr-service/internal/repository"
	"anpr-service/internal/utils"
)

var (
	ErrInvalidInput          = errors.New("invalid input")
	ErrNotFound              = errors.New("not found")
	ErrVehicleNotWhitelisted = errors.New("vehicle not whitelisted")
	ErrDuplicateEvent        = errors.New("duplicate recent event")
	ErrTooManyRows           = errors.New("too many rows for export")
)

type ANPRService struct {
	repo *repository.ANPRRepository
	log  zerolog.Logger
}

func NewANPRService(repo *repository.ANPRRepository, log zerolog.Logger) *ANPRService {
	return &ANPRService{
		repo: repo,
		log:  log,
	}
}

func (s *ANPRService) ProcessIncomingEvent(ctx context.Context, payload anpr.EventPayload, defaultCameraModel string, eventID uuid.UUID, photoURLs []string) (*anpr.ProcessResult, error) {
	if payload.Plate == "" {
		return nil, fmt.Errorf("%w: plate is required", ErrInvalidInput)
	}
	if payload.CameraID == "" {
		return nil, fmt.Errorf("%w: camera_id is required", ErrInvalidInput)
	}
	if payload.EventTime.IsZero() {
		return nil, fmt.Errorf("%w: event_time is required", ErrInvalidInput)
	}

	normalized := utils.NormalizePlate(payload.Plate)
	if normalized == "" {
		return nil, fmt.Errorf("%w: plate cannot be empty after normalization", ErrInvalidInput)
	}

	// Дедупликация: если тот же номер с этой камеры уже был в окне ±5 минут — считаем дублем
	recent, err := s.repo.ExistsRecentEvent(ctx, normalized, payload.CameraID, payload.EventTime, 5*time.Minute)
	if err != nil {
		return nil, fmt.Errorf("failed to check duplicate event: %w", err)
	}
	if recent {
		s.log.Warn().
			Str("plate", normalized).
			Str("camera_id", payload.CameraID).
			Msg("duplicate event detected within 5 minutes, skipping save")
		return nil, ErrDuplicateEvent
	}

	plateID, err := s.repo.GetOrCreatePlate(ctx, normalized, payload.Plate)
	if err != nil {
		s.log.Error().
			Err(err).
			Str("normalized", normalized).
			Str("original", payload.Plate).
			Msg("failed to get or create plate")
		return nil, fmt.Errorf("failed to get or create plate: %w", err)
	}

	s.log.Info().
		Str("plate_id", plateID.String()).
		Str("normalized", normalized).
		Str("original", payload.Plate).
		Msg("plate retrieved or created successfully")

	// Получаем данные о транспорте из vehicles ДО сохранения события
	vehicleData, err := s.repo.GetVehicleByPlate(ctx, normalized)
	if err != nil {
		s.log.Error().
			Err(err).
			Str("plate", normalized).
			Msg("failed to get vehicle data")
		return nil, fmt.Errorf("failed to get vehicle data: %w", err)
	}

	vehicleExists := vehicleData != nil

	// Обновляем данные о транспорте из vehicles, если vehicle найден
	// Приоритет: данные из vehicles > данные от камеры
	if vehicleExists {
		if vehicleData.Brand != "" {
			payload.Vehicle.Brand = vehicleData.Brand
		}
		if vehicleData.Model != "" {
			payload.Vehicle.Model = vehicleData.Model
		}
		if vehicleData.Color != "" {
			payload.Vehicle.Color = vehicleData.Color
		}
		// Year можно сохранить в raw_payload, если нужно
		if payload.RawPayload == nil {
			payload.RawPayload = make(map[string]interface{})
		}
		payload.RawPayload["vehicle_year"] = vehicleData.Year

		s.log.Info().
			Str("plate", normalized).
			Str("brand", vehicleData.Brand).
			Str("model", vehicleData.Model).
			Str("color", vehicleData.Color).
			Float64("body_volume_m3", vehicleData.BodyVolumeM3).
			Msg("vehicle data loaded from vehicles table")
	} else {
		s.log.Warn().
			Str("plate", normalized).
			Msg("vehicle not found in vehicles table (whitelist check failed)")
		// Если машина не найдена в vehicles — не сохраняем событие
		return nil, fmt.Errorf("%w: vehicle not found in vehicles table", ErrVehicleNotWhitelisted)
	}

	cameraModel := payload.CameraModel
	if cameraModel == "" {
		cameraModel = defaultCameraModel
	}

	// Direction: если камера не дала direction или пришёл "unknown",
	// ставим "entry" по умолчанию, чтобы события учитывались в tickets-сервисе.
	dir := strings.ToLower(payload.Direction)
	if dir == "" || dir == "unknown" {
		dir = "entry"
	}
	payload.Direction = dir

	event := &anpr.Event{
		ID:              eventID, // Use pre-generated ID
		PlateID:         plateID,
		EventPayload:    payload,
		NormalizedPlate: normalized,
	}
	event.CameraModel = cameraModel

	// Данные о снеге: сначала используем поля из payload (если они заполнились при парсинге JSON)
	// Если полей нет, пытаемся извлечь из RawPayload (для обратной совместимости)
	// Если и там нет - устанавливаем значения по умолчанию (0, пустые строки)

	// snow_volume_percentage: используем из payload или RawPayload, иначе 0.0
	if payload.SnowVolumePercentage == nil && payload.RawPayload != nil {
		if snowVolumePct, ok := payload.RawPayload["snow_volume_percentage"].(float64); ok {
			event.SnowVolumePercentage = &snowVolumePct
		} else {
			// Значение по умолчанию: 0.0 если снег не обнаружен
			defaultVolume := 0.0
			event.SnowVolumePercentage = &defaultVolume
		}
	} else if payload.SnowVolumePercentage != nil {
		event.SnowVolumePercentage = payload.SnowVolumePercentage
	} else {
		// Значение по умолчанию: 0.0 если снег не обнаружен
		defaultVolume := 0.0
		event.SnowVolumePercentage = &defaultVolume
	}

	// snow_volume_confidence: используем из payload или RawPayload, иначе 0.0
	if payload.SnowVolumeConfidence == nil && payload.RawPayload != nil {
		if snowVolumeConf, ok := payload.RawPayload["snow_volume_confidence"].(float64); ok {
			event.SnowVolumeConfidence = &snowVolumeConf
		} else {
			// Значение по умолчанию: 0.0 если снег не обнаружен
			defaultConfidence := 0.0
			event.SnowVolumeConfidence = &defaultConfidence
		}
	} else if payload.SnowVolumeConfidence != nil {
		event.SnowVolumeConfidence = payload.SnowVolumeConfidence
	} else {
		// Значение по умолчанию: 0.0 если снег не обнаружен
		defaultConfidence := 0.0
		event.SnowVolumeConfidence = &defaultConfidence
	}

	// Вычисляем snow_volume_m3 на основе процента и объема кузова
	// Формула: snow_volume_m3 = (snow_volume_percentage / 100) * body_volume_m3
	// Вычисляем только если есть процент (даже если 0) И vehicle найден И у vehicle есть объем
	if event.SnowVolumePercentage != nil {
		s.log.Info().
			Float64("snow_volume_percentage", *event.SnowVolumePercentage).
			Bool("vehicle_exists", vehicleExists).
			Bool("matched_snow", event.MatchedSnow).
			Msg("checking conditions for volume calculation")

		if vehicleExists && vehicleData.BodyVolumeM3 > 0 {
			volumeM3 := (*event.SnowVolumePercentage / 100.0) * vehicleData.BodyVolumeM3
			event.SnowVolumeM3 = &volumeM3
			s.log.Info().
				Float64("percentage", *event.SnowVolumePercentage).
				Float64("body_volume_m3", vehicleData.BodyVolumeM3).
				Float64("snow_volume_m3", volumeM3).
				Msg("calculated snow volume in m3")
		} else {
			if !vehicleExists {
				s.log.Warn().
					Str("plate", normalized).
					Msg("cannot calculate snow_volume_m3: vehicle not found")
			} else if vehicleData.BodyVolumeM3 <= 0 {
				s.log.Warn().
					Str("plate", normalized).
					Float64("body_volume_m3", vehicleData.BodyVolumeM3).
					Msg("cannot calculate snow_volume_m3: body_volume_m3 is zero or negative")
			}
		}
	} else {
		s.log.Warn().
			Msg("cannot calculate snow_volume_m3: snow_volume_percentage is nil")
	}

	// matched_snow всегда берем из payload (если есть в JSON, иначе из RawPayload)
	if !payload.MatchedSnow && payload.RawPayload != nil {
		if matchedSnow, ok := payload.RawPayload["matched_snow"].(bool); ok {
			event.MatchedSnow = matchedSnow
		} else {
			event.MatchedSnow = false
		}
	} else {
		event.MatchedSnow = payload.MatchedSnow
	}

	// Получаем contractor_id из vehicleData, если транспорт найден
	var contractorID *uuid.UUID
	if vehicleExists && vehicleData != nil {
		contractorID = vehicleData.ContractorID
	}

	polygonID, err := s.repo.ResolvePolygonIDByCameraID(ctx, payload.CameraID)
	if err != nil {
		s.log.Warn().
			Err(err).
			Str("camera_id", payload.CameraID).
			Msg("failed to resolve polygon_id by camera_id")
	}

	// Сохраняем событие с данными из vehicles (если vehicle найден)
	if err := s.repo.CreateANPREvent(ctx, event, contractorID, polygonID); err != nil {
		s.log.Error().
			Err(err).
			Str("plate", normalized).
			Str("camera_id", payload.CameraID).
			Msg("failed to create ANPR event")
		return nil, fmt.Errorf("failed to create ANPR event: %w", err)
	}

	// Сохраняем фотографии (если есть)
	if len(photoURLs) > 0 {
		if err := s.repo.CreateEventPhotos(ctx, eventID, photoURLs); err != nil {
			s.log.Warn().
				Err(err).
				Str("event_id", eventID.String()).
				Int("photos_count", len(photoURLs)).
				Msg("failed to save event photos")
			// Don't fail the whole request if photos fail
		} else {
			s.log.Info().
				Str("event_id", eventID.String()).
				Int("photos_count", len(photoURLs)).
				Msg("saved event photos")
		}
	}

	s.log.Info().
		Str("event_id", event.ID.String()).
		Str("plate_id", plateID.String()).
		Str("plate", normalized).
		Str("raw_plate", payload.Plate).
		Str("camera_id", payload.CameraID).
		Bool("vehicle_exists", vehicleExists).
		Int("photos_count", len(photoURLs)).
		Time("event_time", payload.EventTime).
		Msg("saved ANPR event to database")

	if vehicleExists {
		s.log.Info().
			Str("plate_id", plateID.String()).
			Str("plate", normalized).
			Msg("vehicle found in vehicles table - access granted")
	} else {
		s.log.Info().
			Str("plate_id", plateID.String()).
			Str("plate", normalized).
			Msg("vehicle not found in vehicles table - access denied")
	}

	return &anpr.ProcessResult{
		EventID:       event.ID,
		PlateID:       plateID,
		Plate:         normalized,
		VehicleExists: vehicleExists,
		Hits:          []anpr.ListHit{}, // Оставляем пустым для обратной совместимости
		PhotoURLs:     photoURLs,
	}, nil
}

func (s *ANPRService) FindPlates(ctx context.Context, plateQuery string) ([]PlateInfo, error) {
	normalized := utils.NormalizePlate(plateQuery)
	if normalized == "" {
		return nil, fmt.Errorf("%w: plate query cannot be empty", ErrInvalidInput)
	}

	plates, err := s.repo.FindPlatesByNormalized(ctx, normalized)
	if err != nil {
		return nil, fmt.Errorf("failed to find plates: %w", err)
	}

	result := make([]PlateInfo, 0, len(plates))
	for _, p := range plates {
		lastEventTime, _ := s.repo.GetLastEventTimeForPlate(ctx, p.ID)
		info := PlateInfo{
			ID:            p.ID.String(),
			Number:        p.Number,
			Normalized:    p.Normalized,
			LastEventTime: lastEventTime,
		}
		result = append(result, info)
	}

	return result, nil
}

func (s *ANPRService) FindEvents(ctx context.Context, plateQuery *string, from, to *string, direction *string, limit, offset int) ([]EventInfo, error) {
	var normalizedPlate *string
	if plateQuery != nil {
		normalized := utils.NormalizePlate(*plateQuery)
		if normalized != "" {
			normalizedPlate = &normalized
		}
	}

	var fromTime, toTime *time.Time
	if from != nil && *from != "" {
		t, err := time.Parse(time.RFC3339, *from)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid from time format", ErrInvalidInput)
		}
		fromTime = &t
	}
	if to != nil && *to != "" {
		t, err := time.Parse(time.RFC3339, *to)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid to time format", ErrInvalidInput)
		}
		toTime = &t
	}

	// Валидация direction
	var validatedDirection *string
	if direction != nil && *direction != "" {
		dir := strings.ToLower(strings.TrimSpace(*direction))
		if dir != "entry" && dir != "exit" {
			return nil, fmt.Errorf("%w: direction must be 'entry' or 'exit'", ErrInvalidInput)
		}
		validatedDirection = &dir
	}

	if limit <= 0 {
		limit = 50
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	events, err := s.repo.FindEvents(ctx, normalizedPlate, fromTime, toTime, validatedDirection, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to find events: %w", err)
	}

	result := make([]EventInfo, 0, len(events))
	for _, e := range events {
		var plateID *string
		if e.PlateID != nil {
			id := e.PlateID.String()
			plateID = &id
		}
		var polygonID *string
		if e.PolygonID != nil {
			id := e.PolygonID.String()
			polygonID = &id
		}
		info := EventInfo{
			ID:                e.ID.String(),
			PlateID:           plateID,
			CameraID:          e.CameraID,
			CameraModel:       e.CameraModel,
			Direction:         e.Direction,
			Lane:              e.Lane,
			RawPlate:          e.RawPlate,
			NormalizedPlate:   e.NormalizedPlate,
			Confidence:        e.Confidence,
			VehicleColor:      e.VehicleColor,
			VehicleType:       e.VehicleType,
			VehicleBrand:      e.VehicleBrand,
			VehicleModel:      e.VehicleModel,
			VehicleCountry:    e.VehicleCountry,
			VehiclePlateColor: e.VehiclePlateColor,
			VehicleSpeed:      e.VehicleSpeed,
			SnapshotURL:       e.SnapshotURL,
			EventTime:         e.EventTime,
			SnowVolumeM3:      e.SnowVolumeM3,
			PolygonID:         polygonID,
		}
		result = append(result, info)
	}

	return result, nil
}

// GetEventsByPlateAndTime получает события для внутреннего использования (для tickets-service)
// Использует ту же структуру EventInfo, что и публичный API
func (s *ANPRService) GetEventsByPlateAndTime(ctx context.Context, normalizedPlate string, from, to time.Time, direction *string) ([]EventInfo, error) {
	if normalizedPlate == "" {
		return nil, fmt.Errorf("%w: normalized plate is required", ErrInvalidInput)
	}

	events, err := s.repo.FindEventsByPlateAndTime(ctx, normalizedPlate, from, to, direction)
	if err != nil {
		s.log.Error().
			Err(err).
			Str("plate", normalizedPlate).
			Time("from", from).
			Time("to", to).
			Msg("failed to find events by plate and time")
		return nil, fmt.Errorf("failed to find events: %w", err)
	}

	result := make([]EventInfo, 0, len(events))
	for _, e := range events {
		var plateID *string
		if e.PlateID != nil {
			id := e.PlateID.String()
			plateID = &id
		}
		var polygonID *string
		if e.PolygonID != nil {
			id := e.PolygonID.String()
			polygonID = &id
		}

		info := EventInfo{
			ID:                e.ID.String(),
			PlateID:           plateID,
			CameraID:          e.CameraID,
			CameraModel:       e.CameraModel,
			Direction:         e.Direction,
			Lane:              e.Lane,
			RawPlate:          e.RawPlate,
			NormalizedPlate:   e.NormalizedPlate,
			Confidence:        e.Confidence,
			VehicleColor:      e.VehicleColor,
			VehicleType:       e.VehicleType,
			VehicleBrand:      e.VehicleBrand,
			VehicleModel:      e.VehicleModel,
			VehicleCountry:    e.VehicleCountry,
			VehiclePlateColor: e.VehiclePlateColor,
			VehicleSpeed:      e.VehicleSpeed,
			SnapshotURL:       e.SnapshotURL,
			EventTime:         e.EventTime,
			SnowVolumeM3:      e.SnowVolumeM3,
			PolygonID:         polygonID,
		}
		result = append(result, info)
	}

	s.log.Info().
		Str("plate", normalizedPlate).
		Time("from", from).
		Time("to", to).
		Int("events_count", len(result)).
		Msg("found events by plate and time")

	return result, nil
}

// GetEventByID получает событие по ID вместе с фотографиями
func (s *ANPRService) GetEventByID(ctx context.Context, eventID uuid.UUID) (*EventInfo, error) {
	event, err := s.repo.GetEventByID(ctx, eventID)
	if err != nil {
		s.log.Error().Err(err).Str("event_id", eventID.String()).Msg("failed to get event by id")
		return nil, fmt.Errorf("failed to get event: %w", err)
	}
	if event == nil {
		return nil, ErrNotFound
	}

	// Получаем фотографии события
	photos, err := s.repo.GetEventPhotos(ctx, eventID)
	if err != nil {
		s.log.Warn().Err(err).Str("event_id", eventID.String()).Msg("failed to get event photos")
		// Продолжаем без фото, если ошибка при получении фото
		photos = []repository.EventPhoto{}
	}

	// Преобразуем фото в массив URL
	photoURLs := make([]string, 0, len(photos))
	for _, photo := range photos {
		photoURLs = append(photoURLs, photo.PhotoURL)
	}

	// Получаем данные о водителе и подрядчике
	var driverID, driverFullName, driverIIN, driverPhone *string
	var contractorID, contractorName, contractorBIN *string

	driverData, err := s.repo.GetDriverByVehiclePlate(ctx, event.NormalizedPlate)
	if err != nil {
		s.log.Warn().Err(err).Str("plate", event.NormalizedPlate).Msg("failed to get driver data")
	} else if driverData != nil {
		id := driverData.ID.String()
		driverID = &id
		driverFullName = &driverData.FullName
		driverIIN = &driverData.IIN
		driverPhone = &driverData.Phone
	}

	contractorData, err := s.repo.GetContractorByVehiclePlate(ctx, event.NormalizedPlate)
	if err != nil {
		s.log.Warn().Err(err).Str("plate", event.NormalizedPlate).Msg("failed to get contractor data")
	} else if contractorData != nil {
		id := contractorData.ID.String()
		contractorID = &id
		contractorName = &contractorData.Name
		contractorBIN = &contractorData.BIN
	}

	// Преобразуем событие в EventInfo
	var plateID *string
	if event.PlateID != nil {
		id := event.PlateID.String()
		plateID = &id
	}
	var polygonID *string
	if event.PolygonID != nil {
		id := event.PolygonID.String()
		polygonID = &id
	}

	info := EventInfo{
		ID:                event.ID.String(),
		PlateID:           plateID,
		CameraID:          event.CameraID,
		CameraModel:       event.CameraModel,
		Direction:         event.Direction,
		Lane:              event.Lane,
		RawPlate:          event.RawPlate,
		NormalizedPlate:   event.NormalizedPlate,
		Confidence:        event.Confidence,
		VehicleColor:      event.VehicleColor,
		VehicleType:       event.VehicleType,
		VehicleBrand:      event.VehicleBrand,
		VehicleModel:      event.VehicleModel,
		VehicleCountry:    event.VehicleCountry,
		VehiclePlateColor: event.VehiclePlateColor,
		VehicleSpeed:      event.VehicleSpeed,
		SnapshotURL:       event.SnapshotURL,
		EventTime:         event.EventTime,
		SnowVolumeM3:      event.SnowVolumeM3,
		PolygonID:         polygonID,
		Photos:            photoURLs,
		// Driver and contractor info
		DriverID:       driverID,
		DriverFullName: driverFullName,
		DriverIIN:      driverIIN,
		DriverPhone:    driverPhone,
		ContractorID:   contractorID,
		ContractorName: contractorName,
		ContractorBIN:  contractorBIN,
	}

	return &info, nil
}

// CleanupOldEvents удаляет события старше указанного количества дней
func (s *ANPRService) CleanupOldEvents(ctx context.Context, days int) (int64, error) {
	deleted, err := s.repo.DeleteOldEvents(ctx, days)
	if err != nil {
		s.log.Error().Err(err).Int("days", days).Msg("failed to cleanup old events")
		return 0, err
	}
	if deleted > 0 {
		s.log.Info().Int64("deleted_count", deleted).Int("days", days).Msg("cleaned up old events")
	}
	return deleted, nil
}

// DeleteOldEvents удаляет события старше указанного количества дней
func (s *ANPRService) DeleteOldEvents(ctx context.Context, days int) (int64, error) {
	if days < 1 {
		return 0, fmt.Errorf("%w: days must be >= 1", ErrInvalidInput)
	}

	deletedCount, err := s.repo.DeleteOldEvents(ctx, days)
	if err != nil {
		s.log.Error().
			Err(err).
			Int("days", days).
			Msg("failed to delete old events")
		return 0, fmt.Errorf("failed to delete old events: %w", err)
	}

	s.log.Info().
		Int("days", days).
		Int64("deleted_count", deletedCount).
		Msg("deleted old events")

	return deletedCount, nil
}

// DeleteAllEvents удаляет все события из базы данных
func (s *ANPRService) DeleteAllEvents(ctx context.Context) (int64, error) {
	s.log.Warn().Msg("attempting to delete ALL events from database")

	deletedCount, err := s.repo.DeleteAllEvents(ctx)
	if err != nil {
		s.log.Error().
			Err(err).
			Msg("failed to delete all events")
		return 0, fmt.Errorf("failed to delete all events: %w", err)
	}

	s.log.Warn().
		Int64("deleted_count", deletedCount).
		Msg("successfully deleted ALL events from database")

	return deletedCount, nil
}

// SyncVehicleToWhitelist синхронизирует номер транспортного средства в whitelist
// Вызывается при создании/обновлении vehicle в roles сервисе
func (s *ANPRService) SyncVehicleToWhitelist(ctx context.Context, plateNumber string) (uuid.UUID, error) {
	plateID, err := s.repo.SyncVehicleToWhitelist(ctx, plateNumber)
	if err != nil {
		s.log.Error().Err(err).Str("plate_number", plateNumber).Msg("failed to sync vehicle to whitelist")
		return uuid.Nil, fmt.Errorf("sync vehicle to whitelist: %w", err)
	}

	s.log.Info().
		Str("plate_number", plateNumber).
		Str("plate_id", plateID.String()).
		Msg("vehicle synced to whitelist")

	return plateID, nil
}

type PlateInfo struct {
	ID            string     `json:"id"`
	Number        string     `json:"number"`
	Normalized    string     `json:"normalized"`
	LastEventTime *time.Time `json:"last_event_time,omitempty"`
}

type EventInfo struct {
	ID                string    `json:"id"`
	PlateID           *string   `json:"plate_id,omitempty"`
	CameraID          string    `json:"camera_id"`
	CameraModel       *string   `json:"camera_model,omitempty"`
	Direction         *string   `json:"direction,omitempty"`
	Lane              *int      `json:"lane,omitempty"`
	RawPlate          string    `json:"raw_plate"`
	NormalizedPlate   string    `json:"normalized_plate"`
	Confidence        *float64  `json:"confidence,omitempty"`
	VehicleColor      *string   `json:"vehicle_color,omitempty"`
	VehicleType       *string   `json:"vehicle_type,omitempty"`
	VehicleBrand      *string   `json:"vehicle_brand,omitempty"`
	VehicleModel      *string   `json:"vehicle_model,omitempty"`
	VehicleCountry    *string   `json:"vehicle_country,omitempty"`
	VehiclePlateColor *string   `json:"vehicle_plate_color,omitempty"`
	VehicleSpeed      *float64  `json:"vehicle_speed,omitempty"`
	SnapshotURL       *string   `json:"snapshot_url,omitempty"`
	EventTime         time.Time `json:"event_time"`
	SnowVolumeM3      *float64  `json:"snow_volume_m3,omitempty"`
	PolygonID         *string   `json:"polygon_id,omitempty"`
	Photos            []string  `json:"photos,omitempty"` // URLs фотографий (только для детального просмотра)
	// Driver and contractor info
	DriverID       *string `json:"driver_id,omitempty"`
	DriverFullName *string `json:"driver_full_name,omitempty"`
	DriverIIN      *string `json:"driver_iin,omitempty"`
	DriverPhone    *string `json:"driver_phone,omitempty"`
	ContractorID   *string `json:"contractor_id,omitempty"`
	ContractorName *string `json:"contractor_name,omitempty"`
	ContractorBIN  *string `json:"contractor_bin,omitempty"`
}

// GetReports получает отчеты с фильтрацией
func (s *ANPRService) GetReports(ctx context.Context, filters repository.ReportFilters) (*ReportResult, error) {
	// Получаем статистику
	stats, err := s.repo.GetReportStats(ctx, filters)
	if err != nil {
		s.log.Error().Err(err).Msg("failed to get report stats")
		return nil, fmt.Errorf("failed to get report stats: %w", err)
	}

	// Получаем события
	events, err := s.repo.GetReportEvents(ctx, filters)
	if err != nil {
		s.log.Error().Err(err).Msg("failed to get report events")
		return nil, fmt.Errorf("failed to get report events: %w", err)
	}

	// Преобразуем события в формат для ответа
	reportEvents := make([]ReportEventInfo, 0, len(events))
	for _, e := range events {
		var plateID *string
		if e.PlateID != nil {
			id := e.PlateID.String()
			plateID = &id
		}
		var vehicleID *string
		if e.VehicleID != nil {
			id := e.VehicleID.String()
			vehicleID = &id
		}
		var contractorID *string
		if e.ContractorID != nil {
			id := e.ContractorID.String()
			contractorID = &id
		}
		var polygonID *string
		if e.PolygonID != nil {
			id := e.PolygonID.String()
			polygonID = &id
		}

		reportEvents = append(reportEvents, ReportEventInfo{
			ID:                e.ID.String(),
			EventTime:         e.EventTime,
			PlateNumber:       e.NormalizedPlate,
			RawPlate:          e.RawPlate,
			NormalizedPlate:   e.NormalizedPlate,
			PlateID:           plateID,
			CameraID:          e.CameraID,
			CameraModel:       e.CameraModel,
			Direction:         e.Direction,
			Lane:              e.Lane,
			Confidence:        e.Confidence,
			VehicleColor:      e.VehicleColor,
			VehicleType:       e.VehicleType,
			VehicleBrand:      e.VehicleBrand,
			VehicleModel:      e.VehicleModel,
			VehicleCountry:    e.VehicleCountry,
			VehiclePlateColor: e.VehiclePlateColor,
			VehicleSpeed:      e.VehicleSpeed,
			SnapshotURL:       e.SnapshotURL,
			ContractorID:      contractorID,
			ContractorName:    e.ContractorName,
			PolygonID:         polygonID,
			SnowVolumeM3:      e.SnowVolumeM3,
			PlatePhotoURL:     e.PlatePhotoURL,
			BodyPhotoURL:      e.BodyPhotoURL,
			VehicleID:         vehicleID,
		})
	}

	return &ReportResult{
		TotalVolume: stats.TotalVolume,
		TripCount:   stats.TripCount,
		Events:      reportEvents,
	}, nil
}

// ReportResult содержит результат отчета
type ReportResult struct {
	TotalVolume float64           `json:"total_volume"`
	TripCount   int64             `json:"trip_count"`
	Events      []ReportEventInfo `json:"events"`
}

// ReportEventInfo содержит информацию о событии для отчета
type ReportEventInfo struct {
	ID                string    `json:"id"`
	EventTime         time.Time `json:"event_time"`
	PlateNumber       string    `json:"plate_number"`
	RawPlate          string    `json:"raw_plate"`
	NormalizedPlate   string    `json:"normalized_plate"`
	PlateID           *string   `json:"plate_id,omitempty"`
	CameraID          string    `json:"camera_id"`
	CameraModel       *string   `json:"camera_model,omitempty"`
	Direction         *string   `json:"direction,omitempty"`
	Lane              *int      `json:"lane,omitempty"`
	Confidence        *float64  `json:"confidence,omitempty"`
	VehicleColor      *string   `json:"vehicle_color,omitempty"`
	VehicleType       *string   `json:"vehicle_type,omitempty"`
	VehicleBrand      *string   `json:"vehicle_brand,omitempty"`
	VehicleModel      *string   `json:"vehicle_model,omitempty"`
	VehicleCountry    *string   `json:"vehicle_country,omitempty"`
	VehiclePlateColor *string   `json:"vehicle_plate_color,omitempty"`
	VehicleSpeed      *float64  `json:"vehicle_speed,omitempty"`
	SnapshotURL       *string   `json:"snapshot_url,omitempty"`
	ContractorID      *string   `json:"contractor_id,omitempty"`
	ContractorName    *string   `json:"contractor_name,omitempty"`
	PolygonID         *string   `json:"polygon_id,omitempty"`
	SnowVolumeM3      *float64  `json:"snow_volume_m3,omitempty"`
	PlatePhotoURL     *string   `json:"plate_photo_url,omitempty"`
	BodyPhotoURL      *string   `json:"body_photo_url,omitempty"`
	VehicleID         *string   `json:"vehicle_id,omitempty"`
}

// ExportReportsExcel экспортирует отчеты в Excel файл
func (s *ANPRService) ExportReportsExcel(ctx context.Context, filters repository.ReportFilters) ([]byte, string, error) {
	// Проверяем максимальное количество строк
	if filters.MaxRows > 0 {
		count, err := s.repo.CountReportEventsForExcel(ctx, filters)
		if err != nil {
			return nil, "", fmt.Errorf("failed to count events: %w", err)
		}
		if count > int64(filters.MaxRows) {
			return nil, "", fmt.Errorf("%w: found %d rows, maximum allowed is %d", ErrTooManyRows, count, filters.MaxRows)
		}
	}

	// Используем excelize для создания файла
	excelData, filename, err := s.generateExcelReport(ctx, filters)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate excel report: %w", err)
	}
	return excelData, filename, nil
}

// generateExcelReport создает Excel файл с отчетами
func (s *ANPRService) generateExcelReport(ctx context.Context, filters repository.ReportFilters) ([]byte, string, error) {
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			s.log.Warn().Err(err).Msg("failed to close excel file")
		}
	}()

	sheetName := "ANPR Events"
	_, err := f.NewSheet(sheetName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create sheet: %w", err)
	}
	f.DeleteSheet("Sheet1") // Удаляем дефолтный лист

	sw, err := f.NewStreamWriter(sheetName)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create stream writer: %w", err)
	}

	// Стили
	headerStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create header style: %w", err)
	}

	groupHeaderStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
			Size: 12,
		},
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"#E0E0E0"},
			Pattern: 1,
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create group header style: %w", err)
	}

	// Создаем стиль для даты/времени (будем применять после Flush)
	customNumFmt := "yyyy-mm-dd hh:mm:ss"
	dateTimeStyle, err := f.NewStyle(&excelize.Style{
		CustomNumFmt: &customNumFmt,
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create datetime style: %w", err)
	}

	// Заголовки
	headers := []interface{}{"ТОО", "Машина", "Госномер", "Время события", "Процент", "Объем"}
	cell, err := excelize.CoordinatesToCellName(1, 1)
	if err != nil {
		return nil, "", fmt.Errorf("failed to get cell name: %w", err)
	}
	if err := sw.SetRow(cell, headers, excelize.RowOpts{StyleID: headerStyle}); err != nil {
		return nil, "", fmt.Errorf("failed to set header row: %w", err)
	}

	// Закрепляем верхнюю строку
	if err := f.SetPanes(sheetName, &excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		s.log.Warn().Err(err).Msg("failed to freeze panes")
	}

	// Устанавливаем ширину колонок
	if err := f.SetColWidth(sheetName, "A", "A", 28); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}
	if err := f.SetColWidth(sheetName, "B", "B", 24); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}
	if err := f.SetColWidth(sheetName, "C", "C", 14); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}
	if err := f.SetColWidth(sheetName, "D", "D", 20); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}
	if err := f.SetColWidth(sheetName, "E", "E", 12); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}
	if err := f.SetColWidth(sheetName, "F", "F", 14); err != nil {
		return nil, "", fmt.Errorf("failed to set column width: %w", err)
	}

	// Казахстанский часовой пояс (Asia/Qyzylorda = UTC+5)
	kzLocation := time.FixedZone("Asia/Qyzylorda", 5*60*60)

	// Стиль для итоговых строк
	totalStyle, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{
			Bold: true,
		},
		Fill: excelize.Fill{
			Type:    "pattern",
			Color:   []string{"#E8F4F8"},
			Pattern: 1,
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("failed to create total style: %w", err)
	}

	// Читаем данные порциями
	pageSize := 2000
	offset := 0
	rowNum := 2 // Начинаем с 2-й строки (после заголовка)
	var lastContractorName string
	groupByContractor := filters.ContractorID == nil // Группируем только если contractor_id не указан

	// Статистика для текущей группы
	var currentGroupCount int64
	var currentGroupVolume float64
	var currentGroupStartRow int

	// Общая статистика
	var totalCount int64
	var totalVolume float64

	// Функция для вывода итогов группы
	writeGroupTotal := func(contractorName string, count int64, volume float64, startRow int) error {
		if count == 0 {
			return nil
		}
		// Пустая строка перед итогами
		cell, _ := excelize.CoordinatesToCellName(1, rowNum)
		if err := sw.SetRow(cell, []interface{}{"", "", "", "", "", ""}); err != nil {
			return fmt.Errorf("failed to set empty row: %w", err)
		}
		rowNum++

		// Строка с итогами группы
		totalText := fmt.Sprintf("Итого %s: %d рейсов, %.2f м³", contractorName, count, volume)
		cell, _ = excelize.CoordinatesToCellName(1, rowNum)
		if err := sw.SetRow(cell, []interface{}{totalText, "", "", "", "", ""}, excelize.RowOpts{StyleID: totalStyle}); err != nil {
			return fmt.Errorf("failed to set group total row: %w", err)
		}
		// Объединяем ячейки для итогов (6 колонок)
		mergeEnd, _ := excelize.CoordinatesToCellName(6, rowNum)
		if err := f.MergeCell(sheetName, cell, mergeEnd); err != nil {
			return fmt.Errorf("failed to merge cells: %w", err)
		}
		rowNum++
		return nil
	}

	for {
		events, err := s.repo.GetReportEventsForExcel(ctx, filters, pageSize, offset)
		if err != nil {
			return nil, "", fmt.Errorf("failed to get events: %w", err)
		}

		if len(events) == 0 {
			break
		}

		for _, event := range events {
			contractorName := "Не назначено"
			if event.ContractorName != nil && *event.ContractorName != "" && *event.ContractorName != "Не назначено" {
				contractorName = *event.ContractorName
			}

			// Если группируем по ТОО и contractor_name изменился
			if groupByContractor && contractorName != lastContractorName {
				// Выводим итоги предыдущей группы (если была)
				if lastContractorName != "" && currentGroupCount > 0 {
					if err := writeGroupTotal(lastContractorName, currentGroupCount, currentGroupVolume, currentGroupStartRow); err != nil {
						return nil, "", err
					}
				}

			// Пустая строка перед новой группой (если не первая)
			if lastContractorName != "" {
				cell, _ := excelize.CoordinatesToCellName(1, rowNum)
				if err := sw.SetRow(cell, []interface{}{"", "", "", "", "", ""}); err != nil {
					return nil, "", fmt.Errorf("failed to set empty row: %w", err)
				}
				rowNum++
			}

				// Заголовок новой группы
				groupHeader := fmt.Sprintf("ТОО: %s", contractorName)
				cell, _ := excelize.CoordinatesToCellName(1, rowNum)
				if err := sw.SetRow(cell, []interface{}{groupHeader, "", "", "", "", ""}, excelize.RowOpts{StyleID: groupHeaderStyle}); err != nil {
					return nil, "", fmt.Errorf("failed to set group header row: %w", err)
				}
				// Объединяем ячейки для заголовка группы (6 колонок)
				mergeEnd, _ := excelize.CoordinatesToCellName(6, rowNum)
				if err := f.MergeCell(sheetName, cell, mergeEnd); err != nil {
					return nil, "", fmt.Errorf("failed to merge cells: %w", err)
				}
				rowNum++

				// Сбрасываем статистику для новой группы
				lastContractorName = contractorName
				currentGroupCount = 0
				currentGroupVolume = 0
				currentGroupStartRow = rowNum
			} else if !groupByContractor {
				if lastContractorName == "" {
					lastContractorName = contractorName
					currentGroupStartRow = rowNum
				}
			}

			// Форматируем данные
			vehicleInfo := formatVehicleInfo(event.VehicleBrand, event.VehicleModel)
			plateNumber := formatPlateNumber(event.NormalizedPlate, event.RawPlate)
			eventTimeKZ := event.EventTime.In(kzLocation)
			
			// Форматируем процент и объем отдельно
			percentageStr := formatPercentage(event.SnowVolumePercentage)
			volumeStr := formatVolume(event.SnowVolumeM3)

			// Записываем строку данных
			cell, _ := excelize.CoordinatesToCellName(1, rowNum)
			row := []interface{}{
				contractorName,
				vehicleInfo,
				plateNumber,
				eventTimeKZ, // Excel автоматически распознает time.Time как дату/время
				percentageStr,
				volumeStr,
			}
			if err := sw.SetRow(cell, row); err != nil {
				return nil, "", fmt.Errorf("failed to set data row: %w", err)
			}

			// Обновляем статистику
			currentGroupCount++
			totalCount++
			if event.SnowVolumeM3 != nil {
				currentGroupVolume += *event.SnowVolumeM3
				totalVolume += *event.SnowVolumeM3
			}

			rowNum++
		}

		// Если получили меньше pageSize, значит это последняя порция
		if len(events) < pageSize {
			break
		}

		offset += pageSize

		// Проверяем контекст на отмену
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		default:
		}
	}

	// Выводим итоги последней группы
	if groupByContractor && lastContractorName != "" && currentGroupCount > 0 {
		if err := writeGroupTotal(lastContractorName, currentGroupCount, currentGroupVolume, currentGroupStartRow); err != nil {
			return nil, "", err
		}
	} else if !groupByContractor && currentGroupCount > 0 {
		// Если не группируем, но есть данные - выводим общий итог
		if err := writeGroupTotal("", currentGroupCount, currentGroupVolume, currentGroupStartRow); err != nil {
			return nil, "", err
		}
	}

	// Выводим общий итог в конце
	if totalCount > 0 {
		// Пустая строка перед общим итогом
		cell, _ := excelize.CoordinatesToCellName(1, rowNum)
		if err := sw.SetRow(cell, []interface{}{"", "", "", "", "", ""}); err != nil {
			return nil, "", fmt.Errorf("failed to set empty row: %w", err)
		}
		rowNum++

		// Общий итог
		totalText := fmt.Sprintf("ВСЕГО: %d рейсов, %.2f м³", totalCount, totalVolume)
		cell, _ = excelize.CoordinatesToCellName(1, rowNum)
		if err := sw.SetRow(cell, []interface{}{totalText, "", "", "", "", ""}, excelize.RowOpts{StyleID: totalStyle}); err != nil {
			return nil, "", fmt.Errorf("failed to set total row: %w", err)
		}
		// Объединяем ячейки для общего итога (6 колонок)
		mergeEnd, _ := excelize.CoordinatesToCellName(6, rowNum)
		if err := f.MergeCell(sheetName, cell, mergeEnd); err != nil {
			return nil, "", fmt.Errorf("failed to merge cells: %w", err)
		}
		rowNum++
	}

	// Завершаем потоковую запись
	if err := sw.Flush(); err != nil {
		return nil, "", fmt.Errorf("failed to flush stream writer: %w", err)
	}

	// Применяем формат даты/времени к колонке D (после Flush можно использовать обычные методы)
	// Находим последнюю строку с данными
	lastRow := rowNum - 1
	if lastRow > 1 {
		// Применяем стиль ко всем ячейкам времени (колонка D, строки 2 до lastRow)
		// Используем SetColStyle для применения стиля ко всей колонке (более эффективно)
		colStart, _ := excelize.CoordinatesToCellName(4, 2)
		colEnd, _ := excelize.CoordinatesToCellName(4, lastRow)
		if err := f.SetCellStyle(sheetName, colStart, colEnd, dateTimeStyle); err != nil {
			s.log.Warn().Err(err).Msg("failed to set datetime style for column D")
		}
	}

	// Генерируем имя файла
	filename := generateFilename(filters.From, filters.To)

	// Сохраняем в буфер
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, "", fmt.Errorf("failed to write excel to buffer: %w", err)
	}

	return buf.Bytes(), filename, nil
}

// formatVehicleInfo форматирует информацию о машине (brand + model)
func formatVehicleInfo(brand, model *string) string {
	var parts []string
	if brand != nil && strings.TrimSpace(*brand) != "" {
		parts = append(parts, strings.TrimSpace(*brand))
	}
	if model != nil && strings.TrimSpace(*model) != "" {
		parts = append(parts, strings.TrimSpace(*model))
	}
	result := strings.Join(parts, " ")
	// Убираем двойные пробелы
	result = strings.Join(strings.Fields(result), " ")
	return result
}

// formatPlateNumber форматирует номер: использует normalized_plate, если пустой - raw_plate
func formatPlateNumber(normalized, raw string) string {
	if normalized != "" {
		return normalized
	}
	return raw
}

// formatPercentage форматирует процент заполнения
func formatPercentage(percentage *float64) string {
	if percentage == nil || *percentage <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f%%", *percentage)
}

// formatVolume форматирует объем в м³
func formatVolume(volumeM3 *float64) string {
	if volumeM3 == nil || *volumeM3 <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2f м³", *volumeM3)
}

// generateFilename генерирует имя файла для Excel выгрузки
func generateFilename(from, to time.Time) string {
	// Форматируем даты безопасно (без двоеточий)
	fromStr := from.Format("2006-01-02")
	toStr := to.Format("2006-01-02")
	return fmt.Sprintf("anpr-events_%s_%s.xlsx", fromStr, toStr)
}
