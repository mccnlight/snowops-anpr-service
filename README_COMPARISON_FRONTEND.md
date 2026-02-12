# SnowOps ANPR - Comparison API (Frontend Guide)

Этот документ для фронтенд-разработки по endpoint сравнения периодов:

- `GET /api/v1/reports/comparison`

## 1) Что делает endpoint

Endpoint возвращает сравнение двух периодов:

- текущий период (`current`)
- предыдущий период (`previous`)
- отклонения в процентах (`deviation`)

Поддерживаемые режимы:

- `day` - день к дню
- `week` - неделя к неделе
- `month` - месяц к месяцу

Метрики:

- `total_volume` (объем снега)
- `trip_count` (количество рейсов/событий)

Доп.метрики для `week` и `month`:

- `avg_volume_per_day`
- `avg_trips_per_day`

Цвета отклонения:

- `green` - рост
- `red` - падение
- `gray` - без изменений

## 2) Важная бизнес-логика

В расчете учитываются только записи рабочего окна:

- с `16:00` до `10:00` следующего дня
- таймзона: `Asia/Qyzylorda`

## 3) Параметры запроса

Обязательные:

- `mode=day|week|month`
- `from` (RFC3339)
- `to` (RFC3339)

Опциональные:

- `previous_from` (RFC3339)
- `previous_to` (RFC3339)
- `contractor_id`
- `polygon_id`
- `vehicle_id`
- `plate`

Правило для `previous_*`:

- если не переданы, бэк считает предыдущий период автоматически:
  - `day`: -1 день
  - `week`: -7 дней
  - `month`: -1 месяц
- если передаете вручную, нужно передать обе границы: `previous_from` и `previous_to`

## 4) Примеры запросов

### 4.1 Day (авто previous)

```bash
curl "http://localhost:8010/api/v1/reports/comparison?mode=day&from=2026-02-12T00:00:00Z&to=2026-02-12T23:59:59Z" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### 4.2 Week (авто previous)

```bash
curl "http://localhost:8010/api/v1/reports/comparison?mode=week&from=2026-01-10T00:00:00Z&to=2026-01-16T23:59:59Z" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### 4.3 Month (авто previous)

```bash
curl "http://localhost:8010/api/v1/reports/comparison?mode=month&from=2026-01-01T00:00:00Z&to=2026-01-31T23:59:59Z" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### 4.4 Month (ручной previous)

```bash
curl "http://localhost:8010/api/v1/reports/comparison?mode=month&from=2025-12-01T00:00:00Z&to=2025-12-30T23:59:59Z&previous_from=2026-02-01T00:00:00Z&previous_to=2026-02-28T23:59:59Z" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

## 5) Примеры ответов (реальные тесты)

### 5.1 Day (данных нет)

```json
{
  "data": {
    "mode": "day",
    "current": {
      "from": "2026-02-12T00:00:00Z",
      "to": "2026-02-12T23:59:59Z",
      "total_volume": 0,
      "trip_count": 0
    },
    "previous": {
      "from": "2026-02-11T00:00:00Z",
      "to": "2026-02-11T23:59:59Z",
      "total_volume": 0,
      "trip_count": 0
    },
    "deviation": {
      "volume_percent": 0,
      "trip_percent": 0,
      "volume_color": "gray",
      "trip_color": "gray"
    }
  }
}
```

### 5.2 Week (данные есть)

```json
{
  "data": {
    "mode": "week",
    "current": {
      "from": "2026-01-10T00:00:00Z",
      "to": "2026-01-16T23:59:59Z",
      "total_volume": 21052.69,
      "trip_count": 956,
      "avg_volume_per_day": 3007.5271428571427,
      "avg_trips_per_day": 136.57142857142858
    },
    "previous": {
      "from": "2026-01-03T00:00:00Z",
      "to": "2026-01-09T23:59:59Z",
      "total_volume": 43688.8,
      "trip_count": 2099,
      "avg_volume_per_day": 6241.257142857144,
      "avg_trips_per_day": 299.85714285714283
    },
    "deviation": {
      "volume_percent": -51.81215780703522,
      "trip_percent": -54.45450214387804,
      "avg_volume_percent": -51.81215780703522,
      "avg_trips_percent": -54.454502143878024,
      "volume_color": "red",
      "trip_color": "red",
      "avg_volume_color": "red",
      "avg_trips_color": "red"
    }
  }
}
```

### 5.3 Month (данные есть)

```json
{
  "data": {
    "mode": "month",
    "current": {
      "from": "2026-01-01T00:00:00Z",
      "to": "2026-01-31T23:59:59Z",
      "total_volume": 193420.02,
      "trip_count": 9105,
      "avg_volume_per_day": 6239.355483870967,
      "avg_trips_per_day": 293.7096774193548
    },
    "previous": {
      "from": "2025-12-01T00:00:00Z",
      "to": "2025-12-31T23:59:59Z",
      "total_volume": 65493.61,
      "trip_count": 3269,
      "avg_volume_per_day": 2112.6970967741936,
      "avg_trips_per_day": 105.45161290322581
    },
    "deviation": {
      "volume_percent": 195.32655170481513,
      "trip_percent": 178.52554297950442,
      "avg_volume_percent": 195.32655170481513,
      "avg_trips_percent": 178.52554297950442,
      "volume_color": "green",
      "trip_color": "green",
      "avg_volume_color": "green",
      "avg_trips_color": "green"
    }
  }
}
```

## 6) Интерпретация твоих тестов

По присланным результатам логика работает корректно:

- тесты 1, 5, 6 с нулями - валидный сценарий (в периоде не найдено данных по правилам фильтрации)
- тесты 2, 3, 4 - корректно считают сравнение, проценты и цвета
- ручной `previous_from/previous_to` применяется корректно

## 7) Как использовать на фронте

Рекомендуемый UI:

- режим: `Day / Week / Month`
- период A (синий): from/to
- период B (серый): previous_from/previous_to (опционально)
- кнопка `Compare`

Mapping:

- `current` -> синий блок
- `previous` -> серый блок
- `deviation` -> проценты и цвет

Замечание по полям:

- некоторые поля имеют `omitempty`, поэтому при нуле могут отсутствовать в JSON
- на фронте лучше задавать безопасный fallback: `value ?? 0`

