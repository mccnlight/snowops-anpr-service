package http

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"anpr-service/internal/config"
	"anpr-service/internal/domain/anpr"
	"anpr-service/internal/http/middleware"
	"anpr-service/internal/repository"
	"anpr-service/internal/service"
	"anpr-service/internal/storage"
	"anpr-service/internal/utils"
)

type Handler struct {
	anprService *service.ANPRService
	config      *config.Config
	log         zerolog.Logger
	r2Client    *storage.R2Client
}

func NewHandler(
	anprService *service.ANPRService,
	cfg *config.Config,
	log zerolog.Logger,
	r2Client *storage.R2Client,
) *Handler {
	return &Handler{
		anprService: anprService,
		config:      cfg,
		log:         log,
		r2Client:    r2Client,
	}
}

func (h *Handler) Register(r *gin.Engine, authMiddleware gin.HandlerFunc) {
	// Public endpoints
	public := r.Group("/api/v1")
	{
		public.POST("/anpr/events", h.createANPREvent)
		public.POST("/anpr/hikvision", h.createHikvisionEvent)
		public.GET("/anpr/hikvision", h.checkHikvisionEndpoint) // Для проверки доступности камерой
		public.GET("/camera/status", h.checkCameraStatus)
	}

	// Protected endpoints
	protected := r.Group("/api/v1")
	protected.Use(authMiddleware)
	{
		protected.GET("/plates", h.listPlates)
		protected.GET("/events", h.listEvents)
		protected.GET("/events/:id", h.getEvent)
		protected.POST("/anpr/sync-vehicle", h.syncVehicleToWhitelist)
		protected.DELETE("/anpr/events/old", h.deleteOldEvents)
		protected.DELETE("/anpr/events/all", h.deleteAllEvents)
		protected.GET("/reports", h.getReports)
		protected.GET("/reports/excel", h.exportReportsExcel)
	}

	// Internal endpoints (для межсервисного взаимодействия)
	internal := r.Group("/internal")
	internal.Use(middleware.InternalToken(h.config.Auth.InternalToken))
	{
		internal.GET("/anpr/events", h.getInternalEvents)
	}
}

func (h *Handler) createANPREvent(c *gin.Context) {
	// Parse multipart form (max 50MB for photos)
	if err := c.Request.ParseMultipartForm(50 << 20); err != nil {
		// If not multipart, try JSON (backward compatibility)
		var payload anpr.EventPayload
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("failed to parse request: "+err.Error()))
			return
		}

		if payload.EventTime.IsZero() {
			payload.EventTime = time.Now()
		}

		// Generate event ID upfront
		eventID := uuid.New()

		h.log.Info().
			Str("plate", payload.Plate).
			Str("camera_id", payload.CameraID).
			Msg("processing ANPR event (JSON)")

		result, err := h.anprService.ProcessIncomingEvent(c.Request.Context(), payload, h.config.Camera.Model, eventID, nil)
		if err != nil {
			if errors.Is(err, service.ErrInvalidInput) {
				h.log.Warn().
					Err(err).
					Str("plate", payload.Plate).
					Str("camera_id", payload.CameraID).
					Msg("invalid input for ANPR event")
				c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
				return
			}
			if errors.Is(err, service.ErrDuplicateEvent) {
				h.log.Warn().
					Err(err).
					Str("plate", payload.Plate).
					Str("camera_id", payload.CameraID).
					Msg("duplicate event within 5 minutes, skipping save")
				c.JSON(http.StatusConflict, errorResponse(err.Error()))
				return
			}
			if errors.Is(err, service.ErrVehicleNotWhitelisted) {
				h.log.Warn().
					Err(err).
					Str("plate", payload.Plate).
					Str("camera_id", payload.CameraID).
					Msg("vehicle not in whitelist (vehicles table)")
				c.JSON(http.StatusForbidden, errorResponse(err.Error()))
				return
			}
			h.log.Error().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("failed to process ANPR event")
			c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
			return
		}

		h.log.Info().
			Str("event_id", result.EventID.String()).
			Str("plate_id", result.PlateID.String()).
			Str("plate", result.Plate).
			Int("hits_count", len(result.Hits)).
			Msg("successfully processed and saved ANPR event")

		c.JSON(http.StatusCreated, gin.H{
			"status":         "ok",
			"event_id":       result.EventID,
			"plate_id":       result.PlateID,
			"plate":          result.Plate,
			"vehicle_exists": result.VehicleExists,
			"hits":           result.Hits,
			"photos":         result.PhotoURLs,
		})
		return
	}

	// Handle multipart form data
	eventJSON := c.PostForm("event")
	if eventJSON == "" {
		c.JSON(http.StatusBadRequest, errorResponse("event field is required"))
		return
	}

	// Сначала парсим в map, чтобы сохранить все дополнительные поля
	var eventMap map[string]interface{}
	if err := json.Unmarshal([]byte(eventJSON), &eventMap); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("invalid event JSON: "+err.Error()))
		return
	}

	// Извлекаем известные поля для EventPayload
	var payload anpr.EventPayload
	payloadBytes, _ := json.Marshal(eventMap)
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("invalid event JSON: "+err.Error()))
		return
	}

	// ИЗВЛЕКАЕМ СНЕГОВЫЕ ПОЛЯ НАПРЯМУЮ ИЗ eventMap (Go не парсит строку времени в *time.Time автоматически)
	// Делаем это ДО того, как поля будут исключены из RawPayload
	if payload.SnowVolumePercentage == nil {
		// Пробуем разные типы: float64, int, float32
		var snowVolumePct float64
		var ok bool
		if snowVolumePct, ok = eventMap["snow_volume_percentage"].(float64); !ok {
			if pctInt, okInt := eventMap["snow_volume_percentage"].(int); okInt {
				snowVolumePct = float64(pctInt)
				ok = true
			} else if pctFloat32, okFloat32 := eventMap["snow_volume_percentage"].(float32); okFloat32 {
				snowVolumePct = float64(pctFloat32)
				ok = true
			}
		}
		if ok {
			payload.SnowVolumePercentage = &snowVolumePct
			h.log.Info().Float64("snow_volume_percentage", snowVolumePct).Msg("extracted snow_volume_percentage from eventMap")
		} else {
			// Значение по умолчанию: 0.0 если снег не обнаружен
			defaultVolume := 0.0
			payload.SnowVolumePercentage = &defaultVolume
			h.log.Warn().Interface("snow_volume_percentage_type", eventMap["snow_volume_percentage"]).Msg("snow_volume_percentage not found or wrong type, using default 0.0")
		}
	}
	if payload.SnowVolumeConfidence == nil {
		// Пробуем разные типы: float64, int, float32
		var snowVolumeConf float64
		var ok bool
		if snowVolumeConf, ok = eventMap["snow_volume_confidence"].(float64); !ok {
			if confInt, okInt := eventMap["snow_volume_confidence"].(int); okInt {
				snowVolumeConf = float64(confInt)
				ok = true
			} else if confFloat32, okFloat32 := eventMap["snow_volume_confidence"].(float32); okFloat32 {
				snowVolumeConf = float64(confFloat32)
				ok = true
			}
		}
		if ok {
			payload.SnowVolumeConfidence = &snowVolumeConf
			h.log.Info().Float64("snow_volume_confidence", snowVolumeConf).Msg("extracted snow_volume_confidence from eventMap")
		} else {
			// Значение по умолчанию: 0.0 если снег не обнаружен
			defaultConfidence := 0.0
			payload.SnowVolumeConfidence = &defaultConfidence
			h.log.Warn().Interface("snow_volume_confidence_type", eventMap["snow_volume_confidence"]).Msg("snow_volume_confidence not found or wrong type, using default 0.0")
		}
	}
	if !payload.MatchedSnow {
		if matchedSnow, ok := eventMap["matched_snow"].(bool); ok {
			payload.MatchedSnow = matchedSnow
			h.log.Info().Bool("matched_snow", matchedSnow).Msg("extracted matched_snow from eventMap")
		} else {
			// Значение по умолчанию: false если поле не пришло
			payload.MatchedSnow = false
		}
	}

	// Сохраняем все дополнительные поля в RawPayload
	// (поля, которых нет в структуре EventPayload)
	if payload.RawPayload == nil {
		payload.RawPayload = make(map[string]interface{})
	}

	// Известные поля EventPayload, которые не нужно дублировать в RawPayload
	knownFields := map[string]bool{
		"camera_id": true, "camera_model": true, "plate": true, "confidence": true,
		"direction": true, "lane": true, "event_time": true, "vehicle": true,
		"snapshot_url": true, "raw_payload": true,
		"snow_volume_percentage": true,
		"snow_volume_confidence": true, "snow_volume_m3": true, "matched_snow": true,
	}

	// Добавляем неизвестные поля в RawPayload
	for key, value := range eventMap {
		if !knownFields[key] && value != nil {
			payload.RawPayload[key] = value
		}
	}

	if payload.EventTime.IsZero() {
		payload.EventTime = time.Now()
	}

	// Generate event ID upfront so we can organize photos by event
	eventID := uuid.New()

	// Get photos from form
	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("failed to parse multipart form"))
		return
	}

	photoFiles := form.File["photos"]
	var photoURLs []string

	// Upload photos organized by date, camera_id and time
	if h.r2Client != nil && len(photoFiles) > 0 {
		for i, fileHeader := range photoFiles {
			url, err := h.uploadEventPhoto(c.Request.Context(), fileHeader, eventID, payload.EventTime, payload.CameraID, i)
			if err != nil {
				h.log.Warn().
					Err(err).
					Str("filename", fileHeader.Filename).
					Str("event_id", eventID.String()).
					Msg("failed to upload photo")
				continue
			}
			photoURLs = append(photoURLs, url)
		}
	} else if len(photoFiles) > 0 && h.r2Client == nil {
		h.log.Warn().
			Int("photos_count", len(photoFiles)).
			Msg("photos provided but R2 storage not configured, skipping photo upload")
	}

	h.log.Info().
		Str("plate", payload.Plate).
		Str("camera_id", payload.CameraID).
		Int("photos_count", len(photoURLs)).
		Msg("processing ANPR event with photos")

	result, err := h.anprService.ProcessIncomingEvent(c.Request.Context(), payload, h.config.Camera.Model, eventID, photoURLs)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			h.log.Warn().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("invalid input for ANPR event")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		if errors.Is(err, service.ErrDuplicateEvent) {
			h.log.Warn().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("duplicate event within 5 minutes, skipping save")
			c.JSON(http.StatusConflict, errorResponse(err.Error()))
			return
		}
		if errors.Is(err, service.ErrVehicleNotWhitelisted) {
			h.log.Warn().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("vehicle not in whitelist (vehicles table)")
			c.JSON(http.StatusForbidden, errorResponse(err.Error()))
			return
		}
		h.log.Error().
			Err(err).
			Str("plate", payload.Plate).
			Str("camera_id", payload.CameraID).
			Msg("failed to process ANPR event")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	h.log.Info().
		Str("event_id", result.EventID.String()).
		Str("plate_id", result.PlateID.String()).
		Str("plate", result.Plate).
		Int("hits_count", len(result.Hits)).
		Int("photos_count", len(photoURLs)).
		Msg("successfully processed and saved ANPR event")

	c.JSON(http.StatusCreated, gin.H{
		"status":         "ok",
		"event_id":       result.EventID,
		"plate_id":       result.PlateID,
		"plate":          result.Plate,
		"vehicle_exists": result.VehicleExists,
		"hits":           result.Hits,
		"photos":         result.PhotoURLs,
	})
}

func (h *Handler) uploadEventPhoto(
	ctx context.Context,
	fileHeader *multipart.FileHeader,
	eventID uuid.UUID,
	eventTime time.Time,
	cameraID string,
	index int,
) (string, error) {
	const maxPhotoSize = 10 << 20 // 10MB
	if fileHeader.Size > maxPhotoSize {
		return "", errors.New("photo too large, max 10MB")
	}

	if fileHeader.Size <= 0 {
		return "", errors.New("photo is empty")
	}

	// Open file once - we'll use it for both content type detection and upload
	file, err := fileHeader.Open()
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Validate content type
	contentType := fileHeader.Header.Get("Content-Type")
	if contentType == "" {
		// Try to detect from file
		buf := make([]byte, 512)
		if n, _ := file.Read(buf); n > 0 {
			contentType = http.DetectContentType(buf[:n])
		}
		// Reset file position to beginning for upload
		if seeker, ok := file.(io.Seeker); ok {
			seeker.Seek(0, io.SeekStart)
		} else {
			// If file doesn't support seeking, we need to reopen it
			file.Close()
			file, err = fileHeader.Open()
			if err != nil {
				return "", fmt.Errorf("failed to reopen file: %w", err)
			}
			defer file.Close()
		}
	}

	if contentType == "" {
		contentType = "image/jpeg"
	}

	if !strings.HasPrefix(contentType, "image/") {
		return "", errors.New("file must be an image")
	}

	// Determine file extension
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if ext == "" {
		// Default based on content type
		if strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg") {
			ext = ".jpg"
		} else if strings.Contains(contentType, "png") {
			ext = ".png"
		} else {
			ext = ".jpg" // Default
		}
	}

	// Convert to Kazakhstan timezone (GMT+5)
	kzLocation := time.FixedZone("KZ", 5*60*60) // UTC+5
	eventTimeKZ := eventTime.In(kzLocation)

	// Format date and time for folder structure.
	dateStr := eventTimeKZ.Format("2006-01-02")
	timeStr := eventTimeKZ.Format("15-04-05")
	cameraPath := sanitizePathSegment(cameraID, "unknown_camera")

	// Organize photos by date, camera and time:
	// anpr_events/{YYYY-MM-DD}/{camera_id}/{HH-MM-SS}/{event_id}-photo-{index}{ext}
	key := fmt.Sprintf("anpr_events/%s/%s/%s/%s-photo-%d%s",
		dateStr, cameraPath, timeStr, eventID.String(), index, ext)

	// Upload to R2
	url, err := h.r2Client.Upload(ctx, key, file, fileHeader.Size, contentType)
	if err != nil {
		return "", fmt.Errorf("r2 upload failed: %w", err)
	}

	return url, nil
}

func sanitizePathSegment(value, fallback string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return fallback
	}

	var b strings.Builder
	b.Grow(len(normalized))
	prevUnderscore := false
	for _, r := range normalized {
		isAllowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
		if isAllowed {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}

	sanitized := strings.Trim(b.String(), "_")
	if sanitized == "" {
		return fallback
	}

	return sanitized
}

func (h *Handler) listPlates(c *gin.Context) {
	plateQuery := strings.TrimSpace(c.Query("plate"))
	if plateQuery == "" {
		c.JSON(http.StatusBadRequest, errorResponse("plate parameter is required"))
		return
	}

	plates, err := h.anprService.FindPlates(c.Request.Context(), plateQuery)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().Err(err).Msg("failed to find plates")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	c.JSON(http.StatusOK, successResponse(plates))
}

func (h *Handler) listEvents(c *gin.Context) {
	var plateQuery *string
	if plate := strings.TrimSpace(c.Query("plate")); plate != "" {
		plateQuery = &plate
	}

	var from, to *string
	if f := strings.TrimSpace(c.Query("from")); f != "" {
		from = &f
	}
	if t := strings.TrimSpace(c.Query("to")); t != "" {
		to = &t
	}

	var direction *string
	if d := strings.TrimSpace(c.Query("direction")); d != "" {
		direction = &d
	}

	limit := 10
	if l := c.Query("limit"); l != "" {
		if parsed, err := parseInt(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := parseInt(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	events, err := h.anprService.FindEvents(c.Request.Context(), plateQuery, from, to, direction, limit, offset)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().Err(err).Msg("failed to find events")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	c.JSON(http.StatusOK, successResponse(events))
}

func (h *Handler) getEvent(c *gin.Context) {
	eventIDStr := c.Param("id")
	eventID, err := uuid.Parse(eventIDStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("invalid event id"))
		return
	}

	event, err := h.anprService.GetEventByID(c.Request.Context(), eventID)
	if err != nil {
		if errors.Is(err, service.ErrNotFound) {
			c.JSON(http.StatusNotFound, errorResponse("event not found"))
			return
		}
		h.log.Error().Err(err).Str("event_id", eventID.String()).Msg("failed to get event")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	c.JSON(http.StatusOK, successResponse(event))
}

func (h *Handler) handleError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, service.ErrInvalidInput):
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
	case errors.Is(err, service.ErrNotFound):
		c.JSON(http.StatusNotFound, errorResponse(err.Error()))
	default:
		h.log.Error().Err(err).Msg("handler error")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
	}
}

func (h *Handler) createHikvisionEvent(c *gin.Context) {
	h.log.Info().
		Str("method", c.Request.Method).
		Str("path", c.Request.URL.Path).
		Str("remote_addr", c.ClientIP()).
		Str("user_agent", c.Request.UserAgent()).
		Str("content_type", c.Request.Header.Get("Content-Type")).
		Msg("received Hikvision event request")

	if err := c.Request.ParseMultipartForm(10 << 20); err != nil {
		h.log.Error().Err(err).Msg("failed to parse multipart request")
		c.JSON(http.StatusBadRequest, errorResponse("invalid multipart payload"))
		return
	}

	xmlPayload, err := extractXMLPayload(c.Request.MultipartForm)
	if err != nil {
		h.log.Error().Err(err).Msg("failed to extract xml payload")
		c.JSON(http.StatusBadRequest, errorResponse("xml payload not found"))
		return
	}

	h.log.Debug().
		Int("xml_size", len(xmlPayload)).
		Str("xml_preview", string(xmlPayload[:min(200, len(xmlPayload))])).
		Msg("extracted XML payload")

	hikEvent := &hikvisionEvent{}
	if err := xml.Unmarshal(xmlPayload, hikEvent); err != nil {
		h.log.Error().
			Err(err).
			Str("xml_content", string(xmlPayload)).
			Msg("failed to parse hikvision xml")
		c.JSON(http.StatusBadRequest, errorResponse("invalid xml payload"))
		return
	}

	h.log.Info().
		Str("event_type", hikEvent.EventType).
		Str("license_plate", hikEvent.ANPR.LicensePlate).
		Str("device_id", hikEvent.DeviceID).
		Str("channel_id", hikEvent.ChannelID).
		Str("date_time", hikEvent.DateTime).
		Str("vehicle_info_color", hikEvent.VehicleInfo.Color).
		Str("vehicle_info_brand", hikEvent.VehicleInfo.Brand).
		Str("vehicle_info_logo_recog", hikEvent.VehicleInfo.VehicleLogoRecog).
		Str("vehicle_info_model", hikEvent.VehicleInfo.Model).
		Str("vehicle_info_vehile_model", hikEvent.VehicleInfo.VehileModel).
		Str("gat_color", hikEvent.VehicleGATInfo.ColorByGAT).
		Msg("parsed Hikvision event")

	payload := hikEvent.ToEventPayload(xmlPayload)

	if payload.CameraID == "" {
		cameraID := c.Query("camera_id")
		if cameraID == "" {
			cameraID = h.config.Camera.HTTPHost
		}
		payload.CameraID = cameraID
	}
	if payload.CameraModel == "" {
		payload.CameraModel = h.config.Camera.Model
	}
	if payload.EventTime.IsZero() {
		payload.EventTime = time.Now()
	}
	if payload.RawPayload == nil {
		payload.RawPayload = map[string]interface{}{
			"xml": string(xmlPayload),
		}
	}

	// Generate event ID upfront
	eventID := uuid.New()

	result, err := h.anprService.ProcessIncomingEvent(c.Request.Context(), payload, h.config.Camera.Model, eventID, nil)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			h.log.Warn().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("invalid input for Hikvision event")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		if errors.Is(err, service.ErrVehicleNotWhitelisted) {
			h.log.Warn().
				Err(err).
				Str("plate", payload.Plate).
				Str("camera_id", payload.CameraID).
				Msg("vehicle not in whitelist (vehicles table)")
			c.JSON(http.StatusForbidden, errorResponse(err.Error()))
			return
		}
		h.log.Error().
			Err(err).
			Str("plate", payload.Plate).
			Str("camera_id", payload.CameraID).
			Msg("failed to process hikvision event")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	h.log.Info().
		Str("event_id", result.EventID.String()).
		Str("plate_id", result.PlateID.String()).
		Str("plate", result.Plate).
		Int("hits_count", len(result.Hits)).
		Msg("successfully processed and saved Hikvision event")

	c.JSON(http.StatusCreated, gin.H{
		"status":         "ok",
		"event_id":       result.EventID,
		"plate_id":       result.PlateID,
		"plate":          result.Plate,
		"vehicle_exists": result.VehicleExists,
		"hits":           result.Hits,
		"photos":         result.PhotoURLs,
		"processed":      true,
	})
}

// checkHikvisionEndpoint обрабатывает GET запросы от камеры для проверки доступности эндпоинта
func (h *Handler) checkHikvisionEndpoint(c *gin.Context) {
	h.log.Info().
		Str("method", c.Request.Method).
		Str("path", c.Request.URL.Path).
		Str("remote_addr", c.ClientIP()).
		Str("user_agent", c.Request.UserAgent()).
		Msg("received Hikvision endpoint check request")

	// Возвращаем 200 OK, чтобы камера знала, что эндпоинт доступен
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"message": "Hikvision ANPR endpoint is available",
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func extractXMLPayload(form *multipart.Form) ([]byte, error) {
	if form == nil {
		return nil, errors.New("empty form")
	}

	for _, files := range form.File {
		for _, fh := range files {
			if isXMLFile(fh) {
				file, err := fh.Open()
				if err != nil {
					return nil, err
				}
				defer file.Close()
				return io.ReadAll(file)
			}
		}
	}

	for key, values := range form.Value {
		if strings.Contains(strings.ToLower(key), "xml") && len(values) > 0 {
			return []byte(values[0]), nil
		}
	}

	return nil, errors.New("xml file not found")
}

func isXMLFile(fh *multipart.FileHeader) bool {
	filename := strings.ToLower(fh.Filename)
	if strings.HasSuffix(filename, ".xml") {
		return true
	}
	contentType := strings.ToLower(fh.Header.Get("Content-Type"))
	return strings.Contains(contentType, "xml")
}

type hikvisionEvent struct {
	XMLName          xml.Name `xml:"EventNotificationAlert"`
	EventType        string   `xml:"eventType" json:"event_type"`
	EventDescription string   `xml:"eventDescription" json:"event_description"`
	DateTime         string   `xml:"dateTime" json:"date_time"`
	ChannelID        string   `xml:"channelID" json:"channel_id"`
	DeviceID         string   `xml:"deviceID" json:"device_id"`
	DeviceName       string   `xml:"deviceName" json:"device_name"`
	IPAddress        string   `xml:"ipAddress" json:"ip_address"`
	PortNo           string   `xml:"portNo" json:"port_no"`
	ProtocolType     string   `xml:"protocolType" json:"protocol_type"`
	ANPR             struct {
		LicensePlate    string  `xml:"licensePlate" json:"license_plate"`
		ConfidenceLevel float64 `xml:"confidenceLevel" json:"confidence_level"`
		VehicleType     string  `xml:"vehicleType" json:"vehicle_type"`
		VehicleColor    string  `xml:"vehicleColor" json:"vehicle_color"`
		Color           string  `xml:"color" json:"color"`
		PlateColor      string  `xml:"plateColor" json:"plate_color"`
		Country         string  `xml:"country" json:"country"`
		Brand           string  `xml:"brand" json:"brand"`
		Direction       string  `xml:"direction" json:"direction"`
		LaneNo          string  `xml:"laneNo" json:"lane_no"`
		Speed           string  `xml:"speed" json:"speed"`
	} `xml:"ANPR" json:"anpr"`
	VehicleInfo struct {
		Type             string `xml:"vehicleType" json:"vehicle_type"`
		Color            string `xml:"color" json:"color"`
		VehicleColor     string `xml:"vehicleColor" json:"vehicle_color"`
		Brand            string `xml:"brand" json:"brand"`
		VehicleLogoRecog string `xml:"vehicleLogoRecog" json:"vehicle_logo_recog"`
		Model            string `xml:"vehicleModel" json:"vehicle_model"`
		VehileModel      string `xml:"vehileModel" json:"vehile_model"`
		PlateColor       string `xml:"plateColor" json:"plate_color"`
		Country          string `xml:"country" json:"country"`
		Speed            string `xml:"speed" json:"speed"`
	} `xml:"vehicleInfo" json:"vehicle_info"`
	VehicleGATInfo struct {
		VehicleTypeByGAT string `xml:"vehicleTypeByGAT" json:"vehicle_type_by_gat"`
		ColorByGAT       string `xml:"colorByGAT" json:"color_by_gat"`
		PlateTypeByGAT   string `xml:"palteTypeByGAT" json:"plate_type_by_gat"`
		PlateColorByGAT  string `xml:"plateColorByGAT" json:"plate_color_by_gat"`
	} `xml:"VehicleGATInfo" json:"vehicle_gat_info"`
	PicInfo struct {
		StoragePath string   `xml:"ftpPath" json:"ftp_path"`
		FilePath    string   `xml:"filePath" json:"file_path"`
		FilePaths   []string `xml:"filePathList>filePath" json:"file_path_list"`
	} `xml:"picInfo" json:"pic_info"`
}

func (e *hikvisionEvent) ToEventPayload(rawXML []byte) anpr.EventPayload {
	eventTime := parseHikvisionTime(e.DateTime)
	lane := parseLane(e.ANPR.LaneNo)

	// Цвет: ПРИОРИТЕТ - текстовые значения из vehicleInfo, НЕ используем GAT коды если есть текст
	// GAT коды (H, C и т.д.) - это числовые коды, не читаемые названия
	vehicleColor := firstNonEmpty(
		e.VehicleInfo.Color,        // "blue", "white" - текстовое значение (ПРИОРИТЕТ)
		e.VehicleInfo.VehicleColor, // альтернативное поле в vehicleInfo
		e.ANPR.VehicleColor,        // из ANPR секции (если есть)
		e.ANPR.Color,               // альтернативное поле в ANPR
	)
	// НЕ используем GAT коды - они нечитаемые (H, C и т.д.)
	// Если текстового значения нет, оставляем пустым

	// Тип: сначала из ANPR, потом из GAT, потом из vehicleInfo
	vehicleType := firstNonEmpty(
		e.ANPR.VehicleType,
		e.VehicleGATInfo.VehicleTypeByGAT,
		e.VehicleInfo.Type,
	)
	vehiclePlateColor := firstNonEmpty(
		e.ANPR.PlateColor,
		e.VehicleGATInfo.PlateColorByGAT,
		e.VehicleInfo.PlateColor,
	)
	vehicleCountry := firstNonEmpty(e.ANPR.Country, e.VehicleInfo.Country)

	// Бренд: сначала текстовое значение, потом ID из vehicleLogoRecog
	vehicleBrand := firstNonEmpty(e.VehicleInfo.Brand, e.ANPR.Brand)
	// Если текстового значения нет, но есть ID логотипа, сохраняем ID
	if vehicleBrand == "" && e.VehicleInfo.VehicleLogoRecog != "" && e.VehicleInfo.VehicleLogoRecog != "0" {
		vehicleBrand = "brand_id:" + e.VehicleInfo.VehicleLogoRecog
	}

	// Модель: сначала текстовое значение, потом ID из vehileModel
	vehicleModel := firstNonEmpty(e.VehicleInfo.Model, e.VehicleInfo.VehileModel)
	// Если текстового значения нет, но есть ID модели, сохраняем ID (игнорируем "0")
	if vehicleModel == "" || vehicleModel == "0" {
		// Если есть другой ID модели, используем его
		if e.VehicleInfo.VehileModel != "" && e.VehicleInfo.VehileModel != "0" {
			vehicleModel = "model_id:" + e.VehicleInfo.VehileModel
		} else {
			vehicleModel = ""
		}
	}
	speedPtr := parseOptionalFloat(firstNonEmpty(e.VehicleInfo.Speed, e.ANPR.Speed))

	cameraModel := firstNonEmpty(e.DeviceName, e.DeviceID)
	snapshotURL := firstNonEmpty(e.PicInfo.StoragePath, e.PicInfo.FilePath)
	if snapshotURL == "" && len(e.PicInfo.FilePaths) > 0 {
		snapshotURL = e.PicInfo.FilePaths[0]
	}

	rawPayload := map[string]interface{}{
		"event_type":        e.EventType,
		"event_description": e.EventDescription,
		"device_id":         e.DeviceID,
		"device_name":       e.DeviceName,
		"channel_id":        e.ChannelID,
		"ip_address":        e.IPAddress,
		"port_no":           e.PortNo,
		"protocol_type":     e.ProtocolType,
		"anpr":              e.ANPR,
		"vehicle_info":      e.VehicleInfo,
		"vehicle_gat_info":  e.VehicleGATInfo,
	}
	if len(rawXML) > 0 {
		rawPayload["xml"] = string(rawXML)
	}

	return anpr.EventPayload{
		CameraID:    firstNonEmpty(e.ChannelID, e.DeviceID),
		CameraModel: cameraModel,
		Plate:       strings.TrimSpace(e.ANPR.LicensePlate),
		Confidence:  e.ANPR.ConfidenceLevel,
		Direction:   e.ANPR.Direction,
		Lane:        lane,
		EventTime:   eventTime,
		Vehicle: anpr.VehicleInfo{
			Color:      vehicleColor,
			Type:       vehicleType,
			Brand:      vehicleBrand,
			Model:      vehicleModel,
			Country:    vehicleCountry,
			PlateColor: vehiclePlateColor,
			Speed:      speedPtr,
		},
		SnapshotURL: snapshotURL,
		RawPayload:  rawPayload,
	}
}

func parseHikvisionTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05",
	}

	for _, layout := range layouts {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}

	return time.Time{}
}

func parseLane(value string) int {
	if value == "" {
		return 0
	}
	lane, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return lane
}

func parseOptionalFloat(value string) *float64 {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	if f, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
		return &f
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func successResponse(data interface{}) gin.H {
	return gin.H{
		"data": data,
	}
}

func (h *Handler) syncVehicleToWhitelist(c *gin.Context) {
	var req struct {
		PlateNumber string `json:"plate_number" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
		return
	}

	plateID, err := h.anprService.SyncVehicleToWhitelist(c.Request.Context(), req.PlateNumber)
	if err != nil {
		h.log.Error().Err(err).Str("plate_number", req.PlateNumber).Msg("failed to sync vehicle to whitelist")
		c.JSON(http.StatusInternalServerError, errorResponse("failed to sync vehicle to whitelist"))
		return
	}

	h.log.Info().
		Str("plate_number", req.PlateNumber).
		Str("plate_id", plateID.String()).
		Msg("vehicle synced to whitelist")

	c.JSON(http.StatusOK, gin.H{
		"status":       "ok",
		"plate_id":     plateID.String(),
		"plate_number": req.PlateNumber,
		"message":      "vehicle added to whitelist",
	})
}

func (h *Handler) deleteOldEvents(c *gin.Context) {
	var req struct {
		Days int `json:"days" binding:"required,min=1"`
	}

	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("days parameter is required and must be >= 1"))
		return
	}

	deletedCount, err := h.anprService.DeleteOldEvents(c.Request.Context(), req.Days)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().Err(err).Int("days", req.Days).Msg("failed to delete old events")
		c.JSON(http.StatusInternalServerError, errorResponse("failed to delete old events"))
		return
	}

	h.log.Info().
		Int("days", req.Days).
		Int64("deleted_count", deletedCount).
		Msg("deleted old events")

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"deleted_count": deletedCount,
		"message":       fmt.Sprintf("deleted %d events older than %d days", deletedCount, req.Days),
	})
}

func (h *Handler) deleteAllEvents(c *gin.Context) {
	var req struct {
		Confirm bool `json:"confirm" binding:"required"`
	}

	if err := c.ShouldBindJSON(&req); err != nil || !req.Confirm {
		c.JSON(http.StatusBadRequest, errorResponse("confirmation required: set confirm=true"))
		return
	}

	h.log.Warn().Str("user_ip", c.ClientIP()).Msg("DELETE ALL EVENTS requested")

	deletedCount, err := h.anprService.DeleteAllEvents(c.Request.Context())
	if err != nil {
		h.log.Error().
			Err(err).
			Str("error_details", err.Error()).
			Msg("failed to delete all events")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "failed to delete all events",
			"details": err.Error(),
		})
		return
	}

	h.log.Warn().
		Int64("deleted_count", deletedCount).
		Str("user_ip", c.ClientIP()).
		Msg("successfully deleted ALL events")

	c.JSON(http.StatusOK, gin.H{
		"status":        "ok",
		"deleted_count": deletedCount,
		"message":       fmt.Sprintf("deleted all %d events", deletedCount),
	})
}

func errorResponse(message string) gin.H {
	return gin.H{
		"error": message,
	}
}

// getInternalEvents обрабатывает запрос на получение событий для внутреннего использования
// GET /internal/anpr/events?plate=KZ123ABC&start_time=2025-01-15T10:00:00Z&end_time=2025-01-15T18:00:00Z&direction=entry
func (h *Handler) getInternalEvents(c *gin.Context) {
	plate := strings.TrimSpace(c.Query("plate"))
	if plate == "" {
		c.JSON(http.StatusBadRequest, errorResponse("plate parameter is required"))
		return
	}

	normalizedPlate := utils.NormalizePlate(plate)
	if normalizedPlate == "" {
		c.JSON(http.StatusBadRequest, errorResponse("invalid plate format"))
		return
	}

	startTimeStr := strings.TrimSpace(c.Query("start_time"))
	if startTimeStr == "" {
		c.JSON(http.StatusBadRequest, errorResponse("start_time parameter is required (ISO8601 format)"))
		return
	}

	endTimeStr := strings.TrimSpace(c.Query("end_time"))
	if endTimeStr == "" {
		c.JSON(http.StatusBadRequest, errorResponse("end_time parameter is required (ISO8601 format)"))
		return
	}

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("invalid start_time format, expected ISO8601 (RFC3339)"))
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, errorResponse("invalid end_time format, expected ISO8601 (RFC3339)"))
		return
	}

	if endTime.Before(startTime) {
		c.JSON(http.StatusBadRequest, errorResponse("end_time must be after start_time"))
		return
	}

	var direction *string
	if dir := strings.TrimSpace(c.Query("direction")); dir != "" {
		dir = strings.ToLower(dir)
		if dir != "entry" && dir != "exit" {
			c.JSON(http.StatusBadRequest, errorResponse("direction must be 'entry' or 'exit'"))
			return
		}
		direction = &dir
	}

	events, err := h.anprService.GetEventsByPlateAndTime(c.Request.Context(), normalizedPlate, startTime, endTime, direction)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			h.log.Warn().
				Err(err).
				Str("plate", normalizedPlate).
				Str("start_time", startTimeStr).
				Str("end_time", endTimeStr).
				Msg("invalid input for internal events query")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().
			Err(err).
			Str("plate", normalizedPlate).
			Str("start_time", startTimeStr).
			Str("end_time", endTimeStr).
			Msg("failed to get internal events")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	h.log.Info().
		Str("plate", normalizedPlate).
		Time("start_time", startTime).
		Time("end_time", endTime).
		Int("events_count", len(events)).
		Msg("returning internal events")

	c.JSON(http.StatusOK, successResponse(events))
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(s)
}

func (h *Handler) checkCameraStatus(c *gin.Context) {
	httpHost := h.config.Camera.HTTPHost
	rtspURL := h.config.Camera.RTSPURL
	cameraModel := h.config.Camera.Model

	status := gin.H{
		"camera_model": cameraModel,
		"http_host":    httpHost,
		"rtsp_url":     maskPassword(rtspURL),
		"configured":   httpHost != "" && rtspURL != "",
	}

	// Проверяем доступность HTTP интерфейса камеры
	if httpHost != "" {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get(httpHost)
		if err != nil {
			status["http_accessible"] = false
			status["http_error"] = err.Error()
		} else {
			resp.Body.Close()
			status["http_accessible"] = resp.StatusCode < 500
			status["http_status"] = resp.StatusCode
		}
	} else {
		status["http_accessible"] = false
		status["http_error"] = "HTTP host not configured"
	}

	// RTSP URL проверяем только на наличие (для проверки подключения нужен специальный клиент)
	status["rtsp_configured"] = rtspURL != ""

	h.log.Info().
		Str("http_host", httpHost).
		Bool("http_accessible", status["http_accessible"].(bool)).
		Msg("camera status checked")

	c.JSON(http.StatusOK, gin.H{
		"status": status,
	})
}

func maskPassword(url string) string {
	// Маскируем пароль в URL для безопасности
	if strings.Contains(url, "@") {
		parts := strings.Split(url, "@")
		if len(parts) == 2 {
			authPart := parts[0]
			if strings.Contains(authPart, "://") {
				protocol := strings.Split(authPart, "://")[0]
				credentials := strings.Split(authPart, "://")[1]
				if strings.Contains(credentials, ":") {
					username := strings.Split(credentials, ":")[0]
					return protocol + "://" + username + ":****@" + parts[1]
				}
			}
		}
	}
	return url
}

func (h *Handler) getReports(c *gin.Context) {
	// Получаем Principal для проверки прав доступа
	principal, ok := middleware.MustPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse("unauthorized"))
		return
	}

	// Парсим фильтры из query параметров
	filters := repository.ReportFilters{}

	// Фильтр по подрядчику
	if contractorIDStr := strings.TrimSpace(c.Query("contractor_id")); contractorIDStr != "" {
		contractorID, err := uuid.Parse(contractorIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid contractor_id"))
			return
		}
		filters.ContractorID = &contractorID
	}

	// Фильтр по полигону
	if polygonIDStr := strings.TrimSpace(c.Query("polygon_id")); polygonIDStr != "" {
		polygonID, err := uuid.Parse(polygonIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid polygon_id"))
			return
		}
		filters.PolygonID = &polygonID
	}

	// Фильтр по vehicle_id
	if vehicleIDStr := strings.TrimSpace(c.Query("vehicle_id")); vehicleIDStr != "" {
		vehicleID, err := uuid.Parse(vehicleIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid vehicle_id"))
			return
		}
		filters.VehicleID = &vehicleID
	}

	// Фильтр по номеру (поиск)
	if plateNumber := strings.TrimSpace(c.Query("plate")); plateNumber != "" {
		filters.PlateNumber = &plateNumber
	}

	// Фильтр по периоду
	var fromTime, toTime time.Time
	if fromStr := strings.TrimSpace(c.Query("from")); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid from time format, use RFC3339"))
			return
		}
		fromTime = t
		filters.From = fromTime
	}

	if toStr := strings.TrimSpace(c.Query("to")); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid to time format, use RFC3339"))
			return
		}
		toTime = t
		filters.To = toTime
	}

	// Если период не указан, используем последние 24 часа по умолчанию
	if fromTime.IsZero() && toTime.IsZero() {
		now := time.Now()
		toTime = now
		fromTime = now.AddDate(0, 0, -1) // Последние 24 часа
		filters.From = fromTime
		filters.To = toTime
	}

	// Если указан только один из периодов, используем его как границу
	if !fromTime.IsZero() && toTime.IsZero() {
		filters.To = time.Now()
		toTime = filters.To
	}
	if fromTime.IsZero() && !toTime.IsZero() {
		filters.From = toTime.AddDate(0, 0, -1) // За день до to
		fromTime = filters.From
	}

	// Валидация: to должно быть после from
	if !filters.From.IsZero() && !filters.To.IsZero() {
		if filters.To.Before(filters.From) {
			c.JSON(http.StatusBadRequest, errorResponse("to time must be after from time"))
			return
		}
	}

	// Права доступа: подрядчики видят только свои события
	if principal.IsContractor() {
		// Подрядчик видит только события своих машин
		filters.ContractorID = &principal.OrgID
		filters.OnlyAssigned = true
	} else {
		// Админы/КГУ видят все события, включая непривязанные
		// Если не указан фильтр по подрядчику, показываем все
		filters.OnlyAssigned = false
	}

	// Пагинация
	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := parseInt(l); err == nil && parsed > 0 {
			limit = parsed
			if limit > 1000 {
				limit = 1000 // Максимум 1000 записей
			}
		}
	}
	filters.Limit = limit

	offset := 0
	if o := c.Query("offset"); o != "" {
		if parsed, err := parseInt(o); err == nil && parsed >= 0 {
			offset = parsed
		}
	}
	filters.Offset = offset

	// Получаем отчеты
	result, err := h.anprService.GetReports(c.Request.Context(), filters)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			h.log.Warn().Err(err).Msg("invalid input for reports query")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().Err(err).Msg("failed to get reports")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	c.JSON(http.StatusOK, successResponse(result))
}

func (h *Handler) exportReportsExcel(c *gin.Context) {
	// Получаем Principal для проверки прав доступа
	principal, ok := middleware.MustPrincipal(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, errorResponse("unauthorized"))
		return
	}

	// Парсим фильтры из query параметров (аналогично getReports)
	filters := repository.ReportFilters{}

	// Фильтр по подрядчику
	if contractorIDStr := strings.TrimSpace(c.Query("contractor_id")); contractorIDStr != "" {
		contractorID, err := uuid.Parse(contractorIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid contractor_id"))
			return
		}
		filters.ContractorID = &contractorID
	}

	// Фильтр по полигону
	if polygonIDStr := strings.TrimSpace(c.Query("polygon_id")); polygonIDStr != "" {
		polygonID, err := uuid.Parse(polygonIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid polygon_id"))
			return
		}
		filters.PolygonID = &polygonID
	}

	// Фильтр по vehicle_id
	if vehicleIDStr := strings.TrimSpace(c.Query("vehicle_id")); vehicleIDStr != "" {
		vehicleID, err := uuid.Parse(vehicleIDStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid vehicle_id"))
			return
		}
		filters.VehicleID = &vehicleID
	}

	// Фильтр по номеру (поиск)
	if plateNumber := strings.TrimSpace(c.Query("plate")); plateNumber != "" {
		filters.PlateNumber = &plateNumber
	}

	// Фильтр по периоду
	var fromTime, toTime time.Time
	if fromStr := strings.TrimSpace(c.Query("from")); fromStr != "" {
		t, err := time.Parse(time.RFC3339, fromStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid from time format, use RFC3339"))
			return
		}
		fromTime = t
		filters.From = fromTime
	}

	if toStr := strings.TrimSpace(c.Query("to")); toStr != "" {
		t, err := time.Parse(time.RFC3339, toStr)
		if err != nil {
			c.JSON(http.StatusBadRequest, errorResponse("invalid to time format, use RFC3339"))
			return
		}
		toTime = t
		filters.To = toTime
	}

	// Если период не указан, используем последние 24 часа по умолчанию
	if fromTime.IsZero() && toTime.IsZero() {
		now := time.Now()
		toTime = now
		fromTime = now.AddDate(0, 0, -1) // Последние 24 часа
		filters.From = fromTime
		filters.To = toTime
	}

	// Если указан только один из периодов, используем его как границу
	if !fromTime.IsZero() && toTime.IsZero() {
		filters.To = time.Now()
		toTime = filters.To
	}
	if fromTime.IsZero() && !toTime.IsZero() {
		filters.From = toTime.AddDate(0, 0, -1) // За день до to
		fromTime = filters.From
	}

	// Валидация: to должно быть после from
	if !filters.From.IsZero() && !filters.To.IsZero() {
		if filters.To.Before(filters.From) {
			c.JSON(http.StatusBadRequest, errorResponse("to time must be after from time"))
			return
		}
	}

	// Защита от больших выгрузок: максимум 90 дней
	if !filters.From.IsZero() && !filters.To.IsZero() {
		daysDiff := filters.To.Sub(filters.From).Hours() / 24
		if daysDiff > 90 {
			c.JSON(http.StatusBadRequest, errorResponse("date range cannot exceed 90 days"))
			return
		}
	}

	// Права доступа: подрядчики видят только свои события
	if principal.IsContractor() {
		// Подрядчик видит только события своих машин
		filters.ContractorID = &principal.OrgID
		filters.OnlyAssigned = true
	} else {
		// Админы/КГУ видят все события, включая непривязанные
		// Если не указан фильтр по подрядчику, показываем все
		filters.OnlyAssigned = false
	}

	// Для Excel limit/offset из query НЕ используем - используем внутреннюю пагинацию
	// Но проверяем максимальное количество строк (100k)
	filters.MaxRows = 100000

	// Генерируем Excel файл
	excelData, filename, err := h.anprService.ExportReportsExcel(c.Request.Context(), filters)
	if err != nil {
		if errors.Is(err, service.ErrInvalidInput) {
			h.log.Warn().Err(err).Msg("invalid input for excel export")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		if errors.Is(err, service.ErrTooManyRows) {
			h.log.Warn().Err(err).Msg("too many rows for excel export")
			c.JSON(http.StatusBadRequest, errorResponse(err.Error()))
			return
		}
		h.log.Error().Err(err).Msg("failed to export reports to excel")
		c.JSON(http.StatusInternalServerError, errorResponse("internal error"))
		return
	}

	// Устанавливаем заголовки для скачивания файла
	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Data(http.StatusOK, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", excelData)
}
