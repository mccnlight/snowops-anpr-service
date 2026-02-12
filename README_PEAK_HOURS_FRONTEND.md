# Frontend: Активность по часам суток

Документ описывает интеграцию графика **«Активность по часам суток»**.

## Endpoint

- `GET /api/v1/reports/hourly-activity`
- Требуется JWT: `Authorization: Bearer <token>`

## Логика (важно)

- Рабочее окно: `16:00-10:00` в таймзоне `Asia/Qyzylorda`.
- Часы в ответе отдаются в **UTC**.
- Порядок часов в ответе фиксированный (полный цикл 24 часа):
  - `16,17,18,19,20,21,22,23,0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15`
- Метрики:
  - `total_volume` — суммарный объем снега, м3
  - `trip_count` — количество событий/рейсов
- Если в часе нет данных — возвращается `0`.

## Параметры запроса

- `from` (optional, RFC3339)
- `to` (optional, RFC3339)
- `polygon_id` (optional, UUID)
- `contractor_id` (optional, UUID)
- `vehicle_id` (optional, UUID)
- `plate` (optional, поиск по номеру)

Поведение по умолчанию:

- если `from` и `to` не заданы -> берутся последние 24 часа;
- если задан только `from` -> `to = now`;
- если задан только `to` -> `from = to - 24h`.

## Формат ответа

```json
{
  "data": {
    "from": "2026-01-10T11:00:00Z",
    "to": "2026-01-11T04:59:59Z",
    "items": [
      { "hour": 16, "total_volume": 0, "trip_count": 0 },
      { "hour": 17, "total_volume": 0, "trip_count": 0 },
      { "hour": 18, "total_volume": 0, "trip_count": 0 },
      { "hour": 19, "total_volume": 0, "trip_count": 0 },
      { "hour": 20, "total_volume": 533.09, "trip_count": 26 }
    ]
  }
}
```

## Как рисовать на фронте

- X-axis: `items[].hour` в порядке, который пришел из API (не пересортировывать).
- Переключатель Y:
  - режим 1: `items[].total_volume`
  - режим 2: `items[].trip_count`
- Tooltip: показывать обе метрики.

## Смоук-тесты

### 1) Позитив: задан период

```bash
curl --location --request GET "http://localhost:8080/api/v1/reports/hourly-activity?from=2026-01-10T11:00:00Z&to=2026-01-11T04:59:59Z&polygon_id=<POLYGON_UUID>" \
  --header "Authorization: Bearer <TOKEN>"
```

Ожидаем:

- HTTP `200`
- `data.items.length = 24`
- часы в порядке `16..23,0..15`
- в этом примере должны быть ненулевые значения в `20..4` (UTC)

### 2) Позитив: без дат (дефолтный период)

```bash
curl --location --request GET "http://localhost:8080/api/v1/reports/hourly-activity?polygon_id=<POLYGON_UUID>" \
  --header "Authorization: Bearer <TOKEN>"
```

Ожидаем:

- HTTP `200`
- в ответе заполнены `from` и `to`
- `data.items.length = 24`

### 3) Негатив: неверный диапазон дат

```bash
curl --location --request GET "http://localhost:8080/api/v1/reports/hourly-activity?from=2026-01-31T23:59:59Z&to=2026-01-01T00:00:00Z" \
  --header "Authorization: Bearer <TOKEN>"
```

Ожидаем:

- HTTP `400`
- ошибка: `to time must be after from time`

## SQL-сверка с БД (для QA)

Проверка бакетов часов (UTC) в том же периоде:

```sql
SELECT
  EXTRACT(HOUR FROM (e.event_time AT TIME ZONE 'UTC'))::int AS hour_utc,
  COUNT(*) AS trip_count,
  COALESCE(SUM(e.snow_volume_m3), 0) AS total_volume
FROM anpr_events e
WHERE e.event_time >= TIMESTAMPTZ '2026-01-10 11:00:00+00'
  AND e.event_time <= TIMESTAMPTZ '2026-01-11 04:59:59+00'
  AND e.snow_volume_m3 IS NOT NULL
  AND e.snow_volume_m3 > 0
  AND (
    (e.event_time AT TIME ZONE 'Asia/Qyzylorda')::time >= TIME '16:00:00'
    OR (e.event_time AT TIME ZONE 'Asia/Qyzylorda')::time < TIME '10:00:00'
  )
GROUP BY hour_utc
ORDER BY CASE WHEN hour_utc >= 16 THEN hour_utc ELSE hour_utc + 24 END;
```
