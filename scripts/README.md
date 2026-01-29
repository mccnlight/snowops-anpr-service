# Event Import Script

This script imports vehicle trip events from a CSV file into the ANPR service database.

## Overview

The script performs the following steps:
1. Reads trip data from CSV file
2. Fetches all vehicles from the roles service
3. Matches partial plate numbers from CSV with full plate numbers from database
4. Creates ANPR events for each trip with randomized timestamps

## Prerequisites

- Go 1.21 or higher
- **AKIMAT_ADMIN token** (required to fetch all vehicles)
- Access to roles service: `https://snowops-roles.onrender.com`
- Access to ANPR service: `https://snowops-anpr-service.onrender.com`

## CSV Format

The CSV file should have the following columns:
```
Номер,Наименование техники,Гос. Номер,Объем кузова,Итого рейсов,Итого м3
1,Камаз,1421,17,6,102
2,Камаз,925,,6,0
```

| Column | Description | Required |
|--------|-------------|----------|
| Номер | Serial number | Yes |
| Наименование техники | Vehicle name/brand | Yes |
| Гос. Номер | Partial plate number | Yes |
| Объем кузова | Body volume in m³ | Optional |
| Итого рейсов | Total number of trips | Yes |
| Итого м³ | Total snow volume in m³ | Yes |

## Event Generation Logic

### Timestamps
- **Date**: January 8, 2026
- **Time Range**: 9:00 PM (Jan 8) to 5:00 AM (Jan 9)
- **Distribution**: Random times within the window for each event

### Event Fields

All events use these constants:
- `camera_id`: `"camera-001"`
- `direction`: `"forward"`
- `lane`: `1`
- `vehicle`: `{}`
- `matched_snow`: `true`

### Dynamic Fields (Based on CSV)

#### If `Итого м³` > 0:
- `confidence`: `1.0`
- `snow_volume_percentage`: `100.0`
- `snow_volume_m3`: `Итого м³ / Итого рейсов`
- `snow_volume_confidence`: `1.0`

#### If `Итого м³` = 0:
- `confidence`: `0.70`
- `snow_volume_percentage`: `70.0`
- `snow_volume_m3`: `0.0`
- `snow_volume_confidence`: `0.70`

### Example Event Payloads

**With Snow (TotalM3 > 0):**
```json
{
  "camera_id": "camera-001",
  "event_time": "2026-01-08T21:30:00Z",
  "plate": "624AFE15",
  "confidence": 1.0,
  "direction": "forward",
  "lane": 1,
  "vehicle": {},
  "snow_volume_percentage": 100.0,
  "snow_volume_m3": 17.0,
  "snow_volume_confidence": 1.0,
  "matched_snow": true
}
```

**Without Snow (TotalM3 = 0):**
```json
{
  "camera_id": "camera-001",
  "event_time": "2026-01-09T02:15:00Z",
  "plate": "925ABC01",
  "confidence": 0.7,
  "direction": "forward",
  "lane": 1,
  "vehicle": {},
  "snow_volume_percentage": 70.0,
  "snow_volume_m3": 0.0,
  "snow_volume_confidence": 0.7,
  "matched_snow": true
}
```

## Usage

### 1. Build the script
```bash
cd /home/alisher/projects/snowops-anpr-service/scripts
go build -o import_events import_events.go
```

### 2. Run the script
```bash
./import_events events-qurylys.csv
```

The script will:
1. Prompt for AKIMAT_ADMIN token (or use hardcoded token)
2. Display matched vehicles with statistics
3. Ask for confirmation before creating events
4. Create events and show progress

### 3. Output Example

```
Step 1: Reading CSV file...
✓ Read 10 rows from CSV

Step 2: Fetching all vehicles from roles service...
✓ Fetched 45 vehicles

Step 3: Matching partial plate numbers with full plate numbers...
✓ Matched 8 out of 10 rows

================================================================================
MATCHED VEHICLES
================================================================================

CSV Plate: 1421   -> Full Plate: ABC1421KZ    | Камаз 6520
  Body Volume: 17.0 m³ | Trips: 6 | Total Snow: 102.0 m³
  Average per trip: 17.00 m³

...

================================================================================
SUMMARY
================================================================================
  Total rows in CSV:     10
  Successfully matched:  8
  Not matched:           2
  Total trips (matched): 57
  Total snow volume:     730.0 m³
================================================================================

Do you want to create events for matched vehicles? (yes/no): yes

Step 4: Creating events...

Creating 6 events for ABC1421KZ (17.0 m³ per trip)...
  ✓ Event 1/6 created at 21:15:23
  ✓ Event 2/6 created at 22:45:12
  ✓ Event 3/6 created at 00:30:45
  ✓ Event 4/6 created at 01:55:33
  ✓ Event 5/6 created at 03:12:08
  ✓ Event 6/6 created at 04:33:51
✓ Completed ABC1421KZ: 6 events created

...

✓ Event creation complete!
  Successfully created: 57 events
  Failed: 0 events
  Skipped (no trips): 0 vehicles
```

## How Vehicle-Contractor Association Works

Events are automatically associated with the correct contractor:
1. Each vehicle in the database has a `contractor_id`
2. When an event is created with a plate number, the ANPR service looks up the vehicle
3. The event inherits the `contractor_id` from the vehicle record
4. **No authentication needed** for event creation (public endpoint)

## Plate Matching Logic

The script matches partial plate numbers using:
- **Normalization**: Removes spaces, converts to uppercase
- **Contains logic**: Partial number must be contained in full plate
- **Example**: CSV plate "1421" matches database plate "ABC1421KZ"

### Unmatched Plates

If a CSV plate cannot be matched:
- The row is skipped
- A warning is displayed in the output
- The vehicle is listed in the "NOT MATCHED" section

## Important Notes

1. **AKIMAT_ADMIN Token Required**: Only AKIMAT_ADMIN or AKIMAT_USER roles can fetch ALL vehicles
2. **Vehicles Must Exist**: Plates must exist in the `vehicles` table or events will be rejected
3. **No Duplicate Detection**: The script does not check for existing events - ANPR service handles duplicates
4. **Time Window**: All events are created between 9 PM Jan 8 and 5 AM Jan 9, 2026
5. **Random Distribution**: Event times are randomly distributed within the window

## Troubleshooting

### "connection refused"
Ensure the services are accessible at the configured URLs.

### "unauthorized" (403)
Your token is not AKIMAT_ADMIN. Get an AKIMAT_ADMIN token to fetch all vehicles.

### "vehicle not whitelisted"
The plate number doesn't exist in the vehicles table. Verify the plate exists in the database.

### "duplicate event"
The ANPR service detected a duplicate event within 5 minutes. This is expected behavior.

### No matches found
- Check that the CSV file contains valid partial plate numbers
- Verify that vehicles exist in the database
- Ensure plate numbers are properly formatted (numbers only in CSV)

## Files

- `import_events.go` - Main script source code
- `import_events` - Compiled binary
- `events-qurylys.csv` - Sample CSV data
- `README.md` - This file

