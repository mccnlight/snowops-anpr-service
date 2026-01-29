# Quick Start Guide

## Run the Script

```bash
cd /home/alisher/projects/snowops-anpr-service/scripts
./import_events events-qurylys.csv
```

## What It Does

### 1. Reads CSV Data ✅
Parses your CSV file with columns:
- Номер (serial number)
- Наименование техники (vehicle name)
- Гос. Номер (partial plate, e.g., "1421")
- Объем кузова (body volume)
- Итого рейсов (total trips)
- Итого м³ (total snow volume)

### 2. Fetches All Vehicles ✅
- Connects to roles service
- Requires **AKIMAT_ADMIN token** (already in code)
- Gets all vehicles from all contractors

### 3. Matches Plates ✅
- Finds full plate numbers for partial numbers
- Example: "1421" → "ABC1421KZ"
- Shows matched and unmatched vehicles

### 4. Creates Events ✅
For each trip in the CSV:
- **Camera**: `camera-001`
- **Date/Time**: Random time between 9 PM Jan 8 - 5 AM Jan 9, 2026
- **Direction**: `forward`
- **Lane**: `1`
- **Confidence**: `1.0` (if snow > 0) or `0.70` (if no snow)
- **Snow %**: `100%` (if snow > 0) or `70%` (if no snow)
- **Snow m³**: Total m³ ÷ Number of trips

## Event Examples

### CSV Row:
```
1,Камаз,1421,17,6,102
```

### Creates 6 Events Like:
```json
{
  "camera_id": "camera-001",
  "event_time": "2026-01-08T21:15:23Z",  // Random time
  "plate": "ABC1421KZ",                   // Full plate from DB
  "confidence": 1.0,                       // Because 102 > 0
  "direction": "forward",
  "lane": 1,
  "vehicle": {},
  "snow_volume_percentage": 100.0,        // Because 102 > 0
  "snow_volume_m3": 17.0,                 // 102 ÷ 6 = 17
  "snow_volume_confidence": 1.0,
  "matched_snow": true
}
```

## Expected Results (events-qurylys.csv)

Based on your CSV:

| Plate | Vehicle | Trips | Snow/Trip | Total Events | Confidence |
|-------|---------|-------|-----------|--------------|------------|
| 1421  | Камаз   | 6     | 17.0 m³   | 6            | 1.0        |
| 925   | Камаз   | 6     | 0.0 m³    | 6            | 0.70       |
| 819   | Камаз   | 5     | 18.0 m³   | 5            | 1.0        |
| 624   | Газ     | 7     | 0.0 m³    | 7            | 0.70       |
| 197   | Газ     | 5     | 11.0 m³   | 5            | 1.0        |
| 948   | Шахман  | 7     | 25.0 m³   | 7            | 1.0        |
| 325   | Камаз   | 6     | 0.0 m³    | 6            | 0.70       |
| 909   | Шахман  | 7     | 0.0 m³    | 7            | 0.70       |
| 808   | Шахман  | 7     | 0.0 m³    | 7            | 0.70       |
| 855   | Камаз   | 6     | 18.0 m³   | 6            | 1.0        |

**Total: ~62 events** (if all plates match)

## Key Features

✅ **Automatic Contractor Assignment**: Events inherit contractor_id from vehicle  
✅ **Random Timestamps**: Events spread naturally across 8-hour window  
✅ **No Auth Needed**: ANPR event creation is public endpoint  
✅ **Smart Matching**: Partial plates matched to full plates  
✅ **Error Handling**: Shows unmatched plates, failed events  
✅ **Dry Run**: Shows matches before creating events  

## Common Issues

**"Not matched" warning**
- Plate number doesn't exist in database or partial number too generic
- Script will skip this row

**"vehicle not whitelisted"**
- Plate exists but not in vehicles table
- Event will be rejected by ANPR service

**"duplicate event"**
- ANPR service detects duplicate within 5 minutes
- This is normal protection, not an error

## Next Steps

1. **Review Matches**: Run script, check matched vehicles
2. **Confirm Creation**: Type "yes" when prompted
3. **Monitor Progress**: Watch events being created
4. **Verify in DB**: Check that events appear in database

## Support

For issues:
1. Check README.md for detailed documentation
2. Verify AKIMAT_ADMIN token is valid
3. Ensure services are accessible
4. Check CSV format matches specification

