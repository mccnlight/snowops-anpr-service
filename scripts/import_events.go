package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
	"time"
)

// Vehicle структура для получения данных о транспорте
type Vehicle struct {
	ID           string  `json:"ID"`
	ContractorID string  `json:"ContractorID"`
	PlateNumber  string  `json:"PlateNumber"`
	Brand        string  `json:"Brand"`
	Model        string  `json:"Model"`
	Color        string  `json:"Color"`
	Year         int     `json:"Year"`
	BodyVolumeM3 float64 `json:"BodyVolumeM3"`
	DriverID     *string `json:"DriverID"`
	PhotoURL     *string `json:"PhotoURL"`
	IsActive     bool    `json:"IsActive"`
}

// CSVRow данные из CSV файла
type CSVRow struct {
	Number       string  // Номер
	VehicleName  string  // Наименование техники
	PartialPlate string  // Гос. Номер (частичный)
	BodyVolume   float64 // Объем кузова
	TotalTrips   int     // Итого рейсов
	TotalM3      float64 // Итого м3
	Photo1       string  // URL первой фотографии
	Photo2       string  // URL второй фотографии
}

// EventPayload структура для создания события
type EventPayload struct {
	CameraID             string                 `json:"camera_id"`
	EventTime            string                 `json:"event_time"`
	Plate                string                 `json:"plate"`
	Confidence           float64                `json:"confidence"`
	Direction            string                 `json:"direction"`
	Lane                 int                    `json:"lane"`
	Vehicle              map[string]interface{} `json:"vehicle"`
	SnowVolumePercentage float64                `json:"snow_volume_percentage"`
	SnowVolumeM3         float64                `json:"snow_volume_m3"`
	SnowVolumeConfidence float64                `json:"snow_volume_confidence"`
	MatchedSnow          bool                   `json:"matched_snow"`
}

const (
	// API endpoints
	rolesServiceURL = "https://snowops-roles.onrender.com"        // Roles service
	anprServiceURL  = "https://snowops-anpr-service.onrender.com" // ANPR service

	// Event constants
	cameraID  = "camera-001"
	direction = "forward"
	lane      = 1
)

var (
	// Auth token for roles service (replace with actual token)
	authToken = ""
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run import_events.go <path-to-csv> [yyyy-mm-dd]")
		fmt.Println("Example: go run import_events.go events-qurylys.csv 2026-01-20")
		os.Exit(1)
	}

	csvPath := os.Args[1]
	dateStr := "2026-01-29" // Default date
	if len(os.Args) > 2 {
		dateStr = os.Args[2]
	}

	// Parse date
	parsedDate, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		fmt.Printf("Error: Invalid date format '%s'. Please use YYYY-MM-DD.\n", dateStr)
		os.Exit(1)
	}
	fmt.Printf("Target Date: 8PM %s -> 6AM Next Day\n", parsedDate.Format("2006-01-02"))

	// Prompt for auth token if not set
	if authToken == "" {
		fmt.Print("Enter auth token (Bearer token): ")
		fmt.Scanln(&authToken)
	}

	fmt.Println("Step 1: Reading CSV file...")
	rows, err := readCSV(csvPath)
	if err != nil {
		fmt.Printf("Error reading CSV: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Read %d rows from CSV\n", len(rows))

	fmt.Println("\nStep 2: Fetching all vehicles from roles service...")
	vehicles, err := getAllVehicles()
	if err != nil {
		fmt.Printf("Error fetching vehicles: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ Fetched %d vehicles\n", len(vehicles))

	fmt.Println("\nStep 3: Matching partial plate numbers with full plate numbers...")
	matches := matchVehicles(rows, vehicles)
	fmt.Printf("✓ Matched %d out of %d rows\n", len(matches), len(rows))

	// Display matches
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("MATCHED VEHICLES")
	fmt.Println(strings.Repeat("=", 80))

	for csvRow, vehicle := range matches {
		fmt.Printf("\nCSV Plate: %-6s -> Full Plate: %-12s | %s %s\n",
			csvRow.PartialPlate,
			vehicle.PlateNumber,
			vehicle.Brand,
			vehicle.Model)
		fmt.Printf("  Body Volume: %.1f m³ | Trips: %d | Total Snow: %.1f m³\n",
			vehicle.BodyVolumeM3, csvRow.TotalTrips, csvRow.TotalM3)
		if csvRow.TotalTrips > 0 {
			// Always use full body volume from vehicle data
			avgPerTrip := vehicle.BodyVolumeM3
			fmt.Printf("  Average per trip: %.2f m³\n", avgPerTrip)
		}
	}

	// Show unmatched rows
	unmatchedCount := len(rows) - len(matches)
	if unmatchedCount > 0 {
		fmt.Println("\n" + strings.Repeat("=", 80))
		fmt.Printf("⚠ WARNING: %d ROWS NOT MATCHED\n", unmatchedCount)
		fmt.Println(strings.Repeat("=", 80))
		for _, row := range rows {
			if _, found := matches[row]; !found {
				fmt.Printf("  Plate: %-6s | Vehicle: %s\n", row.PartialPlate, row.VehicleName)
			}
		}
	}

	// Summary
	fmt.Println("\n" + strings.Repeat("=", 80))
	fmt.Println("SUMMARY")
	fmt.Println(strings.Repeat("=", 80))
	fmt.Printf("  Total rows in CSV:     %d\n", len(rows))
	fmt.Printf("  Successfully matched:  %d\n", len(matches))
	fmt.Printf("  Not matched:           %d\n", unmatchedCount)

	totalTrips := 0
	totalSnowVolume := 0.0
	for csvRow := range matches {
		totalTrips += csvRow.TotalTrips
		totalSnowVolume += csvRow.TotalM3
	}
	fmt.Printf("  Total trips (matched): %d\n", totalTrips)
	fmt.Printf("  Total snow volume:     %.1f m³\n", totalSnowVolume)
	fmt.Println(strings.Repeat("=", 80))

	// Ask user if they want to create events
	fmt.Print("\nDo you want to create events for matched vehicles? (yes/no): ")
	var response string
	fmt.Scanln(&response)

	if strings.ToLower(strings.TrimSpace(response)) != "yes" {
		fmt.Println("Event creation cancelled.")
		return
	}

	fmt.Println("\nStep 4: Creating events...")
	successCount, failCount, skippedCount := createEventsFromMatches(matches, parsedDate)

	fmt.Printf("\n✓ Event creation complete!\n")
	fmt.Printf("  Successfully created: %d events\n", successCount)
	if failCount > 0 {
		fmt.Printf("  Failed: %d events\n", failCount)
	}
	if skippedCount > 0 {
		fmt.Printf("  Skipped (no trips): %d vehicles\n", skippedCount)
	}
}

// readCSV читает CSV файл и возвращает массив строк
func readCSV(path string) ([]CSVRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	reader := csv.NewReader(file)

	// Skip header
	_, err = reader.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}

	var rows []CSVRow
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to read record: %w", err)
		}

		// Skip empty rows
		if len(record) < 6 || strings.TrimSpace(record[0]) == "" {
			continue
		}

		row := CSVRow{
			Number:       strings.TrimSpace(record[0]),
			VehicleName:  strings.TrimSpace(record[1]),
			PartialPlate: strings.TrimSpace(record[2]),
		}

		// Parse body volume (may be empty)
		if vol := strings.TrimSpace(record[3]); vol != "" {
			if parsed, err := strconv.ParseFloat(vol, 64); err == nil {
				row.BodyVolume = parsed
			}
		}

		// Parse total trips
		if trips := strings.TrimSpace(record[4]); trips != "" {
			if parsed, err := strconv.Atoi(trips); err == nil {
				row.TotalTrips = parsed
			}
		}

		// Parse total m3
		if m3 := strings.TrimSpace(record[5]); m3 != "" {
			if parsed, err := strconv.ParseFloat(m3, 64); err == nil {
				row.TotalM3 = parsed
			}
		}

		// Parse photo URLs (optional columns 7 and 8)
		if len(record) > 6 {
			row.Photo1 = strings.TrimSpace(record[6])
		}
		if len(record) > 7 {
			row.Photo2 = strings.TrimSpace(record[7])
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// getAllVehicles получает все транспортные средства из roles service
func getAllVehicles() ([]Vehicle, error) {
	url := fmt.Sprintf("%s/roles/vehicles?only_active=true", rolesServiceURL)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+authToken)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Vehicles []Vehicle `json:"vehicles"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return result.Vehicles, nil
}

// matchVehicles сопоставляет частичные номера с полными
func matchVehicles(rows []CSVRow, vehicles []Vehicle) map[CSVRow]Vehicle {
	matches := make(map[CSVRow]Vehicle)

	for _, row := range rows {
		if row.PartialPlate == "" {
			continue
		}

		// Ищем транспорт, у которого PlateNumber содержит частичный номер
		for _, vehicle := range vehicles {
			// Нормализуем номера для сравнения (удаляем пробелы, приводим к верхнему регистру)
			normalizedPlate := strings.ToUpper(strings.ReplaceAll(vehicle.PlateNumber, " ", ""))
			normalizedPartial := strings.ToUpper(strings.ReplaceAll(row.PartialPlate, " ", ""))

			// Проверяем, содержится ли частичный номер в полном номере
			if strings.Contains(normalizedPlate, normalizedPartial) {
				matches[row] = vehicle
				break
			}
		}
	}

	return matches
}

// createEventsFromMatches создает события для всех совпадений
func createEventsFromMatches(matches map[CSVRow]Vehicle, date time.Time) (successCount, failCount, skippedCount int) {
	// Seed random with current time
	rand.Seed(time.Now().UnixNano())

	// Time window: [Date] 8 PM to [Date+1] 6 AM
	startDate := time.Date(date.Year(), date.Month(), date.Day(), 20, 0, 0, 0, time.UTC)
	// End date is 10 hours later (6 AM next day)
	endDate := startDate.Add(10 * time.Hour)
	totalWindowSeconds := endDate.Sub(startDate).Seconds()

	// Collect all events to create
	type EventToCreate struct {
		csvRow      CSVRow
		vehicle     Vehicle
		tripIndex   int
		confidence  float64
		snowPercent float64
		snowVolume  float64
		photoURLs   []string
	}

	var eventsToCreate []EventToCreate

	// Process all matches
	for csvRow, vehicle := range matches {
		if csvRow.TotalTrips == 0 {
			fmt.Printf("⊘ Skipping %s (no trips)\n", vehicle.PlateNumber)
			skippedCount++
			continue
		}

		// Check if CSV has photos
		var photoURLs []string
		if csvRow.Photo1 != "" {
			photoURLs = append(photoURLs, csvRow.Photo1)
		}
		if csvRow.Photo2 != "" {
			photoURLs = append(photoURLs, csvRow.Photo2)
		}
		hasPhotosInCSV := len(photoURLs) > 0

		fmt.Printf("\nPreparing %d events for %s (photos in CSV: %v)...\n",
			csvRow.TotalTrips, vehicle.PlateNumber, hasPhotosInCSV)

		// Calculate values for all trips
		for i := 0; i < csvRow.TotalTrips; i++ {
			confidence := 0.95 + (rand.Float64() * 0.05)

			// Random snow percentage between 95% and 100%
			snowPercentage := 95.0 + (rand.Float64() * 5.0)
			// Calculate actual volume based on percentage
			snowVolumePerTrip := vehicle.BodyVolumeM3 * (snowPercentage / 100.0)

			// Attach photos to the last trip if available (legacy logic, preserved)
			isLastTrip := (i == csvRow.TotalTrips-1)
			var eventPhotos []string
			if hasPhotosInCSV && isLastTrip {
				eventPhotos = photoURLs
			}

			event := EventToCreate{
				csvRow:      csvRow,
				vehicle:     vehicle,
				tripIndex:   i,
				confidence:  confidence,
				snowPercent: snowPercentage,
				snowVolume:  snowVolumePerTrip,
				photoURLs:   eventPhotos,
			}
			eventsToCreate = append(eventsToCreate, event)
		}
	}

	// Calculate time slots
	if len(eventsToCreate) == 0 {
		return 0, 0, skippedCount
	}

	// Group events by vehicle plate
	eventsByVehicle := make(map[string][]EventToCreate)
	for _, event := range eventsToCreate {
		plate := event.vehicle.PlateNumber
		eventsByVehicle[plate] = append(eventsByVehicle[plate], event)
	}

	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Printf("Creating %d total events across %d vehicle(s)\n", len(eventsToCreate), len(eventsByVehicle))
	fmt.Printf("Time Window: %s -> %s (%.1f hours)\n",
		startDate.Format("Jan 02 15:04"), endDate.Format("Jan 02 15:04"), totalWindowSeconds/3600)
	fmt.Printf(strings.Repeat("=", 80) + "\n\n")

	minGapSeconds := 5 * 60.0 // 5 minutes minimum gap between events for same vehicle
	eventIndex := 0

	// Process each vehicle's events separately
	for _, vehicleEvents := range eventsByVehicle {
		// Calculate time slots for this vehicle's events
		vehicleSlotDuration := totalWindowSeconds / float64(len(vehicleEvents))

		// Ensure minimum 5-minute gap logic is respected in slot calculation
		// If the slot is too small, events will inevitably be closer than desired or we push bounds.
		// However, with 8pm-6am (10h) and typical trip counts, this is rarely an issue.
		if vehicleSlotDuration < minGapSeconds {
			fmt.Printf("Warning: High frequency for %s, slots forced to %.0fs\n",
				vehicleEvents[0].vehicle.PlateNumber, minGapSeconds)
			vehicleSlotDuration = minGapSeconds
		}

		// Distribute this vehicle's events across the window with proper spacing
		for i, event := range vehicleEvents {
			// Calculate slot start
			slotStart := float64(i) * vehicleSlotDuration

			// We want 'random' placement but maintaining order and minGap.
			// Simple approach: Place somewhere in [slotStart, slotStart + slotDuration - minGap]
			// validity check:
			maxOffset := vehicleSlotDuration - minGapSeconds
			if maxOffset < 0 {
				maxOffset = 0
			}

			randomOffset := rand.Float64() * maxOffset
			randomTimestamp := slotStart + randomOffset
			eventTime := startDate.Add(time.Duration(randomTimestamp) * time.Second)

			// Create the event
			err := createEvent(
				event.vehicle.PlateNumber,
				eventTime,
				event.confidence,
				event.snowPercent,
				event.snowVolume,
				event.photoURLs,
			)

			eventIndex++
			if err != nil {
				fmt.Printf("  ✗ Event %d/%d failed: %v\n", eventIndex, len(eventsToCreate), err)
				failCount++
			} else {
				successCount++
				hasPhotos := "no"
				if len(event.photoURLs) > 0 {
					hasPhotos = fmt.Sprintf("yes (%d)", len(event.photoURLs))
				}
				fmt.Printf("  ✓ Event %d/%d created at %s (Photos: %s)\n",
					eventIndex, len(eventsToCreate), eventTime.Format("15:04:05"), hasPhotos)
			}
		}
	}

	return successCount, failCount, skippedCount
}

// downloadImage downloads an image from URL and returns the bytes
func downloadImage(url string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d when downloading image", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read image data: %w", err)
	}

	return data, nil
}

// createEvent создает одно событие ANPR
func createEvent(
	plate string,
	eventTime time.Time,
	confidence float64,
	snowPercentage float64,
	snowVolumeM3 float64,
	photoURLs []string,
) error {
	url := fmt.Sprintf("%s/api/v1/anpr/events", anprServiceURL)

	payload := EventPayload{
		CameraID:             cameraID,
		EventTime:            eventTime.Format(time.RFC3339),
		Plate:                plate,
		Confidence:           confidence,
		Direction:            direction,
		Lane:                 lane,
		Vehicle:              make(map[string]interface{}),
		SnowVolumePercentage: snowPercentage,
		SnowVolumeM3:         snowVolumeM3,
		SnowVolumeConfidence: confidence, // Same as confidence
		MatchedSnow:          true,
	}

	// Prepare multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add event JSON as form field
	eventJSON, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	eventField, err := writer.CreateFormField("event")
	if err != nil {
		return fmt.Errorf("failed to create form field: %w", err)
	}
	if _, err := eventField.Write(eventJSON); err != nil {
		return fmt.Errorf("failed to write event field: %w", err)
	}

	// Download and add photos
	if len(photoURLs) > 0 {
		for i, photoURL := range photoURLs {
			if photoURL == "" {
				continue
			}

			// Download image
			imageData, err := downloadImage(photoURL)
			if err != nil {
				fmt.Printf("    ⚠ Warning: failed to download photo %d (%s): %v\n", i+1, photoURL, err)
				continue
			}

			// Detect content type from image data
			contentType := http.DetectContentType(imageData)
			if !strings.HasPrefix(contentType, "image/") {
				// Fallback based on URL extension
				if strings.HasSuffix(strings.ToLower(photoURL), ".png") {
					contentType = "image/png"
				} else {
					contentType = "image/jpeg"
				}
			}

			// Determine filename based on content type
			filename := fmt.Sprintf("photo-%d.jpg", i+1)
			if strings.Contains(contentType, "png") {
				filename = fmt.Sprintf("photo-%d.png", i+1)
			}

			// Create form file with proper headers
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="photos"; filename="%s"`, filename))
			h.Set("Content-Type", contentType)

			photoField, err := writer.CreatePart(h)
			if err != nil {
				return fmt.Errorf("failed to create form file: %w", err)
			}

			if _, err := photoField.Write(imageData); err != nil {
				return fmt.Errorf("failed to write photo data: %w", err)
			}
		}
	}

	// Close multipart writer
	if err := writer.Close(); err != nil {
		return fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create request
	req, err := http.NewRequest("POST", url, &body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := &http.Client{Timeout: 60 * time.Second} // Longer timeout for image downloads
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}
