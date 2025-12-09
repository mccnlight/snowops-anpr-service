# SnowOps ANPR Service

Backend-сервис для работы с ANPR-камерой Hikvision (модель DS-TCG406-E), который принимает события распознавания гос. номеров, обрабатывает их, сохраняет в базу данных и предоставляет REST API для поиска и управления событиями.

## Возможности

- Приём webhook-событий от ANPR-камеры (JSON и XML форматы)
- Парсинг и сохранение событий с распознанными номерами
- Нормализация гос. номеров (удаление пробелов, дефисов, приведение к верхнему регистру)
- Проверка номеров по таблице `vehicles` (whitelist)
- Загрузка фотографий в R2 Storage (Cloudflare)
- Поиск событий и номеров через REST API
- Синхронизация транспорта с whitelist
- Анализ объёма снега в кузове (если включено)
- Автоматическая очистка старых событий (старше 3 дней, каждые 6 часов)

## Технологии

- Go 1.22+
- GORM + PostgreSQL
- Gin (HTTP router)
- Zerolog (логирование)
- Viper (конфигурация)
- Cloudflare R2 (хранение фотографий)

## Структура проекта

```
snowops-anpr-service/
├── cmd/
│   └── anpr-service/
│       └── main.go              # Точка входа приложения
├── internal/
│   ├── auth/                    # JWT парсер для авторизации
│   ├── config/                  # Конфигурация из переменных окружения
│   ├── db/                      # Подключение к БД и миграции
│   ├── domain/                  # Доменные модели (Event, VehicleInfo, etc.)
│   ├── http/                    # HTTP handlers и router
│   │   └── middleware/          # Middleware для авторизации и внутренних токенов
│   ├── logger/                  # Логгер (zerolog)
│   ├── model/                   # Общие модели (Principal, UserRole)
│   ├── repository/             # Репозитории для работы с БД
│   ├── service/                 # Бизнес-логика (ANPRService)
│   ├── storage/                 # Клиент для R2 Storage
│   └── utils/                   # Утилиты (нормализация номеров)
├── Dockerfile
├── docker-compose.yml
├── app.env                      # Конфигурация (не коммитится)
└── README.md
```

## Запуск

### Локально

1. Убедитесь, что PostgreSQL запущен
2. Скопируйте `.env.example` в `app.env` и настройте параметры
3. Запустите сервис:

```bash
go run cmd/anpr-service/main.go
```

### Docker Compose

```bash
docker compose up --build
```

Сервис будет доступен на `http://localhost:8082`

## Конфигурация

Все параметры настраиваются через переменные окружения (см. `app.env`):

| Переменная | Описание | Обязательно | По умолчанию |
|------------|----------|-------------|--------------|
| `APP_ENV` | Окружение (`development` / `production`) | Нет | `development` |
| `HTTP_HOST` | Хост для HTTP сервера | Нет | `0.0.0.0` |
| `HTTP_PORT` | Порт для HTTP сервера | Нет | `8082` |
| `DB_DSN` | Строка подключения к PostgreSQL | Да | - |
| `JWT_ACCESS_SECRET` | Секрет для JWT токенов | Да | - |
| `INTERNAL_TOKEN` | Внутренний токен для межсервисного взаимодействия | Да | - |
| `CAMERA_RTSP_URL` | RTSP URL камеры | Нет | - |
| `CAMERA_HTTP_HOST` | HTTP хост камеры | Нет | - |
| `CAMERA_MODEL` | Модель камеры | Нет | `DS-TCG406-E` |
| `HIK_CONNECT_DOMAIN` | Домен HikConnect | Нет | - |
| `ENABLE_SNOW_VOLUME_ANALYSIS` | Включить анализ объёма снега | Нет | `false` |

### R2 Storage (опционально, для загрузки фотографий)

| Переменная | Описание | Обязательно |
|------------|----------|-------------|
| `R2_ENDPOINT` | R2 endpoint URL (например, `https://{account-id}.r2.cloudflarestorage.com`) | Нет |
| `R2_ACCESS_KEY_ID` | Access Key ID для R2 | Нет |
| `R2_SECRET_ACCESS_KEY` | Secret Access Key для R2 | Нет |
| `R2_BUCKET` | Название bucket в R2 | Нет |
| `R2_REGION` | Регион (по умолчанию `auto`) | Нет |
| `R2_PUBLIC_BASE_URL` | Публичный URL для CDN (опционально, если используется CDN перед R2) | Нет |

Если R2 не настроен, сервис будет работать без возможности загрузки фотографий.

## База данных

Сервис создаёт следующие таблицы (миграции выполняются автоматически при старте):

- `anpr_plates` - номера (исходный и нормализованный)
- `anpr_events` - события распознавания
- `anpr_event_photos` - фотографии событий
- `anpr_lists` - списки (whitelist/blacklist)
- `anpr_list_items` - элементы списков

Сервис также использует таблицу `vehicles` из общей схемы SnowOps для проверки номеров.

## API Endpoints

Все эндпоинты возвращают JSON. Формат ответа:
- Успешный ответ: `{ "data": ... }` или `{ "status": "ok", ... }`
- Ошибка: `{ "error": "описание ошибки" }`

### Health Checks

#### `GET /health/live`

Проверка работоспособности сервиса (без проверки БД).

**Ответ:**
```json
{
  "status": "ok"
}
```

#### `GET /health/ready`

Проверка готовности сервиса (включая проверку подключения к БД).

**Ответ:**
```json
{
  "status": "ok"
}
```

**Ошибки:**
- `503 Service Unavailable` - если БД недоступна

---

### Публичные эндпоинты (без авторизации)

Эти эндпоинты используются камерами для отправки событий и не требуют JWT токена.

#### `POST /api/v1/anpr/events`

Приём события от ANPR-камеры. Поддерживает два формата: JSON (для обратной совместимости) и multipart/form-data (с фотографиями).

**Content-Type:** `application/json` или `multipart/form-data`

**Формат 1: JSON (обратная совместимость)**

**Request Body:**
```json
{
  "camera_id": "camera-001",
  "camera_model": "DS-TCG406-E",
  "plate": "123 ABC 02",
  "confidence": 0.95,
  "direction": "enter",
  "lane": 1,
  "event_time": "2025-01-21T12:34:56Z",
  "vehicle": {
    "color": "white",
    "type": "car",
    "brand": "Toyota",
    "model": "Camry",
    "country": "KZ",
    "plate_color": "white",
    "speed": 45.5
  },
  "snapshot_url": "http://camera/snapshot.jpg",
  "snow_volume_percentage": 75.5,
  "snow_volume_confidence": 0.92,
  "matched_snow": true,
  "raw_payload": {
    "additional_field": "value"
  }
}
```

**Формат 2: Multipart Form Data (с фотографиями)**

**Поля формы:**
- `event` (обязательно) - JSON строка с данными события (формат как в JSON выше)
- `photos` (опционально) - файлы фотографий (можно передать несколько)

**Пример запроса с фотографиями:**
```bash
curl -X POST http://localhost:8082/api/v1/anpr/events \
  -F "event={\"camera_id\":\"camera-001\",\"plate\":\"123 ABC 02\",\"event_time\":\"2025-01-21T12:34:56Z\"}" \
  -F "photos=@photo1.jpg" \
  -F "photos=@photo2.jpg" \
  -F "photos=@photo3.jpg"
```

**Параметры запроса:**

| Параметр | Тип | Обязательно | Описание |
|----------|-----|-------------|----------|
| `camera_id` | string | Да | Идентификатор камеры |
| `plate` | string | Да | Номер машины (любой формат, будет нормализован) |
| `event_time` | string (RFC3339) | Нет | Время события (по умолчанию текущее время) |
| `camera_model` | string | Нет | Модель камеры |
| `confidence` | float64 | Нет | Уверенность распознавания (0.0-1.0) |
| `direction` | string | Нет | Направление движения: `enter` (въезд) или `exit` (выезд) |
| `lane` | int | Нет | Номер полосы |
| `vehicle.color` | string | Нет | Цвет автомобиля |
| `vehicle.type` | string | Нет | Тип автомобиля |
| `vehicle.brand` | string | Нет | Марка автомобиля |
| `vehicle.model` | string | Нет | Модель автомобиля |
| `vehicle.country` | string | Нет | Страна регистрации |
| `vehicle.plate_color` | string | Нет | Цвет номерного знака |
| `vehicle.speed` | float64 | Нет | Скорость (км/ч) |
| `snapshot_url` | string | Нет | URL снимка с камеры |
| `snow_volume_percentage` | float64 | Нет | Процент заполнения кузова снегом (0-100) |
| `snow_volume_confidence` | float64 | Нет | Уверенность определения объёма снега (0.0-1.0) |
| `matched_snow` | bool | Нет | Обнаружен ли снег в кузове |
| `raw_payload` | object | Нет | Дополнительные поля для хранения |

**Обработка события:**

1. Номер нормализуется (удаляются пробелы, дефисы, приводится к верхнему регистру)
2. Проверяется наличие номера в таблице `vehicles` (whitelist)
3. Если транспорт найден, данные из `vehicles` (brand, model, color, body_volume_m3) имеют приоритет над данными от камеры
4. Вычисляется объём снега в м³: `snow_volume_m3 = (snow_volume_percentage / 100) * body_volume_m3` (только если транспорт найден и есть body_volume_m3)
5. Событие сохраняется в БД
6. Фотографии загружаются в R2 (если настроено и переданы)

**Структура хранения фотографий в R2:**
```
anpr-events/{YYYY-MM-DD}/{HH-MM-SS}-{event_id}-{normalized_plate}/photo-{index}.{ext}
```

**Пример пути:**
```
anpr-events/2025-01-21/12-34-56-550e8400-e29b-41d4-a716-446655440000-123ABC02/photo-0.jpg
```

**Примечание:** Время в пути использует часовой пояс Казахстана (GMT+5)

**Ответ:**
```json
{
  "status": "ok",
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "plate_id": "660e8400-e29b-41d4-a716-446655440001",
  "plate": "123ABC02",
  "vehicle_exists": true,
  "hits": [],
  "photos": [
    "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-0.jpg",
    "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-1.jpg"
  ]
}
```

**Ошибки:**
- `400 Bad Request` - невалидные данные (отсутствует plate, camera_id, невалидный формат времени)
- `500 Internal Server Error` - внутренняя ошибка сервера

**Примечания:**
- Если R2 не настроен, фотографии будут проигнорированы (событие всё равно сохранится)
- Максимальный размер одной фотографии: 10MB
- Максимальный размер всего запроса: 50MB
- Фотографии сохраняются в R2 с организацией по event_id для удобного управления

#### `POST /api/v1/anpr/hikvision`

Приём события от камеры Hikvision в формате XML (multipart/form-data).

**Content-Type:** `multipart/form-data`

**Request Body:** XML файл в multipart форме (камера отправляет автоматически)

**Пример XML:**
```xml
<EventNotificationAlert>
  <eventType>ANPR</eventType>
  <dateTime>2025-01-21T12:34:56Z</dateTime>
  <deviceID>camera-001</deviceID>
  <channelID>1</channelID>
  <ANPR>
    <licensePlate>123 ABC 02</licensePlate>
    <confidenceLevel>0.95</confidenceLevel>
    <direction>enter</direction>
    <laneNo>1</laneNo>
  </ANPR>
  <vehicleInfo>
    <color>white</color>
    <vehicleType>car</vehicleType>
    <brand>Toyota</brand>
  </vehicleInfo>
</EventNotificationAlert>
```

**Обработка:**
1. XML парсится в структуру события
2. Данные преобразуются в `EventPayload`
3. Событие обрабатывается так же, как в `/api/v1/anpr/events`

**Ответ:**
```json
{
  "status": "ok",
  "event_id": "550e8400-e29b-41d4-a716-446655440000",
  "plate_id": "660e8400-e29b-41d4-a716-446655440001",
  "plate": "123ABC02",
  "vehicle_exists": true,
  "hits": [],
  "photos": [],
  "processed": true
}
```

**Ошибки:**
- `400 Bad Request` - невалидный XML или отсутствует XML в запросе
- `500 Internal Server Error` - внутренняя ошибка сервера

#### `GET /api/v1/anpr/hikvision`

Проверка доступности эндпоинта камерой Hikvision. Камера периодически отправляет GET запросы для проверки доступности сервиса.

**Ответ:**
```json
{
  "status": "ok",
  "message": "Hikvision ANPR endpoint is available"
}
```

#### `GET /api/v1/camera/status`

Получение статуса камеры и проверка её доступности.

**Ответ:**
```json
{
  "status": {
    "camera_model": "DS-TCG406-E",
    "http_host": "http://192.168.1.100",
    "rtsp_url": "rtsp://admin:****@192.168.1.100:554",
    "configured": true,
    "http_accessible": true,
    "http_status": 200,
    "rtsp_configured": true
  }
}
```

**Поля ответа:**
- `camera_model` - модель камеры из конфигурации
- `http_host` - HTTP хост камеры
- `rtsp_url` - RTSP URL (пароль замаскирован)
- `configured` - настроена ли камера (есть ли http_host и rtsp_url)
- `http_accessible` - доступен ли HTTP интерфейс камеры
- `http_status` - HTTP статус код ответа камеры
- `rtsp_configured` - настроен ли RTSP URL

---

### Защищенные эндпоинты (требуют авторизацию)

Все эти эндпоинты требуют JWT токен в заголовке:
```
Authorization: Bearer <JWT_TOKEN>
```

JWT токен должен быть выдан сервисом `snowops-auth-service` и содержать информацию о пользователе (user_id, org_id, role).

#### `GET /api/v1/plates`

Поиск номеров по запросу (частичное совпадение по нормализованному номеру).

**Query параметры:**

| Параметр | Тип | Обязательно | Описание |
|----------|-----|-------------|----------|
| `plate` | string | Да | Номер для поиска (любой формат, будет нормализован) |

**Пример запроса:**
```
GET /api/v1/plates?plate=123ABC02
Authorization: Bearer <JWT_TOKEN>
```

**Ответ:**
```json
{
  "data": [
    {
      "id": "660e8400-e29b-41d4-a716-446655440001",
      "number": "123 ABC 02",
      "normalized": "123ABC02",
      "last_event_time": "2025-01-21T12:34:56Z"
    }
  ]
}
```

**Ошибки:**
- `400 Bad Request` - отсутствует параметр `plate` или номер невалиден после нормализации
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `500 Internal Server Error` - внутренняя ошибка сервера

#### `GET /api/v1/events`

Поиск событий с фильтрацией по номеру, времени, направлению и пагинацией.

**Query параметры:**

| Параметр | Тип | Обязательно | Описание |
|----------|-----|-------------|----------|
| `plate` | string | Нет | Номер машины для фильтрации (любой формат, будет нормализован) |
| `from` | string (RFC3339) | Нет | Начало временного диапазона (например, `2025-01-01T00:00:00Z`) |
| `to` | string (RFC3339) | Нет | Конец временного диапазона (например, `2025-01-31T23:59:59Z`) |
| `direction` | string | Нет | Направление движения: `entry` (въезд) или `exit` (выезд) |
| `limit` | int | Нет | Количество результатов (по умолчанию 50, максимум 100) |
| `offset` | int | Нет | Смещение для пагинации (по умолчанию 0) |

**Пример запроса:**
```
GET /api/v1/events?plate=123ABC02&from=2025-01-01T00:00:00Z&to=2025-01-31T23:59:59Z&direction=entry&limit=50&offset=0
Authorization: Bearer <JWT_TOKEN>
```

**Ответ:**
```json
{
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "plate_id": "660e8400-e29b-41d4-a716-446655440001",
      "camera_id": "camera-001",
      "camera_model": "DS-TCG406-E",
      "normalized_plate": "123ABC02",
      "raw_plate": "123 ABC 02",
      "event_time": "2025-01-21T12:34:56Z",
      "confidence": 0.95,
      "direction": "enter",
      "lane": 1,
      "vehicle_color": "white",
      "vehicle_type": "car",
      "vehicle_brand": "Toyota",
      "vehicle_model": "Camry",
      "vehicle_country": "KZ",
      "vehicle_plate_color": "white",
      "vehicle_speed": 45.5,
      "snapshot_url": "http://camera/snapshot.jpg",
      "snow_volume_m3": 12.5,
      "polygon_id": "770e8400-e29b-41d4-a716-446655440002"
    }
  ]
}
```

**Ошибки:**
- `400 Bad Request` - невалидные параметры (неправильный формат времени, невалидное направление)
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `500 Internal Server Error` - внутренняя ошибка сервера

**Примечания:**
- События сортируются по времени (от новых к старым)
- Если `limit` не указан, возвращается 50 результатов
- Максимальный `limit` - 100

#### `GET /api/v1/events/:id`

Получение события по ID вместе с фотографиями.

**Path параметры:**

| Параметр | Тип | Обязательно | Описание |
|----------|-----|-------------|----------|
| `id` | string (UUID) | Да | ID события |

**Пример запроса:**
```
GET /api/v1/events/550e8400-e29b-41d4-a716-446655440000
Authorization: Bearer <JWT_TOKEN>
```

**Ответ:**
```json
{
  "data": {
    "id": "550e8400-e29b-41d4-a716-446655440000",
    "plate_id": "660e8400-e29b-41d4-a716-446655440001",
    "camera_id": "camera-001",
    "camera_model": "DS-TCG406-E",
    "normalized_plate": "123ABC02",
    "raw_plate": "123 ABC 02",
    "event_time": "2025-01-21T12:34:56Z",
    "confidence": 0.95,
    "direction": "enter",
    "lane": 1,
    "vehicle_color": "white",
    "vehicle_type": "car",
    "vehicle_brand": "Toyota",
    "vehicle_model": "Camry",
    "vehicle_country": "KZ",
    "vehicle_plate_color": "white",
    "vehicle_speed": 45.5,
    "snapshot_url": "http://camera/snapshot.jpg",
    "snow_volume_m3": 12.5,
    "polygon_id": "770e8400-e29b-41d4-a716-446655440002",
    "photos": [
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-0.jpg",
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-1.jpg"
    ]
  }
}
```

**Ошибки:**
- `400 Bad Request` - невалидный UUID
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `404 Not Found` - событие не найдено
- `500 Internal Server Error` - внутренняя ошибка сервера

**Примечания:**
- Если фото отсутствуют, поле `photos` будет пустым массивом
- Фотографии сортируются по `display_order`

#### `POST /api/v1/anpr/sync-vehicle`

Синхронизация номера транспортного средства в whitelist. Вызывается при создании/обновлении vehicle в roles сервисе.

**Request Body:**
```json
{
  "plate_number": "123 ABC 02"
}
```

**Пример запроса:**
```
POST /api/v1/anpr/sync-vehicle
Authorization: Bearer <JWT_TOKEN>
Content-Type: application/json

{
  "plate_number": "123 ABC 02"
}
```

**Ответ:**
```json
{
  "status": "ok",
  "plate_id": "660e8400-e29b-41d4-a716-446655440001",
  "plate_number": "123ABC02",
  "message": "vehicle added to whitelist"
}
```

**Ошибки:**
- `400 Bad Request` - отсутствует `plate_number` в теле запроса
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `500 Internal Server Error` - ошибка синхронизации

**Примечания:**
- Номер автоматически нормализуется
- Используется функция БД `anpr_sync_vehicle_to_whitelist()` для синхронизации

#### `DELETE /api/v1/anpr/events/old`

Удаление событий старше указанного количества дней.

**Request Body:**
```json
{
  "days": 30
}
```

**Пример запроса:**
```
DELETE /api/v1/anpr/events/old
Authorization: Bearer <JWT_TOKEN>
Content-Type: application/json

{
  "days": 30
}
```

**Ответ:**
```json
{
  "status": "ok",
  "deleted_count": 1250,
  "message": "deleted 1250 events older than 30 days"
}
```

**Ошибки:**
- `400 Bad Request` - отсутствует `days` или `days < 1`
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `500 Internal Server Error` - ошибка удаления

**Примечания:**
- Удаляются события, у которых `created_at < (текущее_время - days дней)`
- Фотографии удаляются автоматически благодаря ON DELETE CASCADE

#### `DELETE /api/v1/anpr/events/all`

Удаление всех событий из базы данных. **ОПАСНАЯ ОПЕРАЦИЯ!**

**Request Body:**
```json
{
  "confirm": true
}
```

**Пример запроса:**
```
DELETE /api/v1/anpr/events/all
Authorization: Bearer <JWT_TOKEN>
Content-Type: application/json

{
  "confirm": true
}
```

**Ответ:**
```json
{
  "status": "ok",
  "deleted_count": 50000,
  "message": "deleted all 50000 events"
}
```

**Ошибки:**
- `400 Bad Request` - отсутствует `confirm` или `confirm != true`
- `401 Unauthorized` - отсутствует или невалидный JWT токен
- `500 Internal Server Error` - ошибка удаления

**Примечания:**
- Требует явного подтверждения (`confirm: true`)
- Удаляются все события и связанные фотографии
- Операция логируется с уровнем WARN

---

### Внутренние эндпоинты (для межсервисного взаимодействия)

Эти эндпоинты защищены внутренним токеном (`INTERNAL_TOKEN`) и используются для взаимодействия между сервисами SnowOps.

#### `GET /internal/anpr/events`

Получение событий ANPR для расчета объема снега (используется tickets-service).

**Заголовки:**
```
X-Internal-Token: <INTERNAL_TOKEN>
```

Или можно передать токен через query параметр:
```
?internal_token=<INTERNAL_TOKEN>
```

**Query параметры:**

| Параметр | Тип | Обязательно | Описание |
|----------|-----|-------------|----------|
| `plate` | string | Да | Номер машины (любой формат, будет нормализован) |
| `start_time` | string (RFC3339) | Да | Начало временного диапазона в формате RFC3339 (ISO8601) |
| `end_time` | string (RFC3339) | Да | Конец временного диапазона в формате RFC3339 (ISO8601) |
| `direction` | string | Нет | Направление движения: `entry` (въезд) или `exit` (выезд) |

**Пример запроса:**
```
GET /internal/anpr/events?plate=KZ123ABC02&start_time=2025-01-15T10:00:00Z&end_time=2025-01-15T18:00:00Z&direction=entry
X-Internal-Token: <INTERNAL_TOKEN>
```

**Ответ:**
```json
{
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "plate_id": "660e8400-e29b-41d4-a716-446655440001",
      "camera_id": "camera-001",
      "normalized_plate": "KZ123ABC02",
      "raw_plate": "KZ 123 ABC 02",
      "event_time": "2025-01-15T12:34:56Z",
      "direction": "entry",
      "snow_volume_m3": 42.5,
      "polygon_id": "770e8400-e29b-41d4-a716-446655440002",
      "confidence": 0.95,
      "lane": 1,
      "vehicle_color": "white",
      "vehicle_type": "car"
    }
  ]
}
```

**Ошибки:**
- `400 Bad Request` - отсутствуют обязательные параметры, невалидный формат времени, `end_time` раньше `start_time`, невалидное направление
- `401 Unauthorized` - отсутствует или невалидный внутренний токен
- `500 Internal Server Error` - внутренняя ошибка сервера

**Примечания:**
- Возвращает полную информацию о событиях, включая `snow_volume_m3` и `polygon_id`
- Номер машины автоматически нормализуется
- События сортируются по времени (от старых к новым)
- Если событий нет, возвращается пустой массив `[]`

---

## Работа сервиса

### Обработка входящих событий

1. **Приём события** (`POST /api/v1/anpr/events` или `POST /api/v1/anpr/hikvision`)
   - Парсинг JSON или XML
   - Валидация обязательных полей (plate, camera_id)
   - Генерация UUID для события

2. **Нормализация номера**
   - Удаление пробелов, дефисов
   - Приведение к верхнему регистру
   - Пример: `"123 ABC 02"` → `"123ABC02"`

3. **Проверка whitelist**
   - Поиск номера в таблице `vehicles` (где `is_active = true`)
   - Если транспорт найден:
     - Данные из `vehicles` (brand, model, color, body_volume_m3) имеют приоритет
     - Вычисляется `snow_volume_m3 = (snow_volume_percentage / 100) * body_volume_m3`
   - Если транспорт не найден:
     - Используются данные от камеры
     - `snow_volume_m3` не вычисляется

4. **Сохранение события**
   - Создание или получение записи в `anpr_plates`
   - Сохранение события в `anpr_events`
   - Сохранение фотографий в `anpr_event_photos` (если есть)

5. **Загрузка фотографий в R2** (если настроено)
   - Валидация размера (макс. 10MB на фото)
   - Определение типа контента
   - Загрузка в структуру: `anpr-events/{YYYY-MM-DD}/{HH-MM-SS}-{event_id}-{normalized_plate}/photo-{index}.{ext}`
   - Сохранение URL в БД

### Методы сервиса

#### `ProcessIncomingEvent`

Обрабатывает входящее событие от камеры.

**Параметры:**
- `ctx context.Context` - контекст
- `payload anpr.EventPayload` - данные события
- `defaultCameraModel string` - модель камеры по умолчанию
- `eventID uuid.UUID` - предгенерированный UUID события
- `photoURLs []string` - URLs загруженных фотографий

**Возвращает:**
- `*anpr.ProcessResult` - результат обработки (event_id, plate_id, plate, vehicle_exists, hits, photos)
- `error` - ошибка обработки

**Логика:**
1. Валидация обязательных полей
2. Нормализация номера
3. Получение или создание записи в `anpr_plates`
4. Получение данных о транспорте из `vehicles`
5. Обновление данных события данными из `vehicles` (если транспорт найден)
6. Вычисление `snow_volume_m3` (если возможно)
7. Сохранение события в БД
8. Сохранение фотографий

#### `FindPlates`

Поиск номеров по запросу.

**Параметры:**
- `ctx context.Context` - контекст
- `plateQuery string` - запрос для поиска

**Возвращает:**
- `[]PlateInfo` - список найденных номеров с информацией о последнем событии
- `error` - ошибка поиска

#### `FindEvents`

Поиск событий с фильтрацией и пагинацией.

**Параметры:**
- `ctx context.Context` - контекст
- `plateQuery *string` - номер для фильтрации (опционально)
- `from *string` - начало временного диапазона (RFC3339, опционально)
- `to *string` - конец временного диапазона (RFC3339, опционально)
- `direction *string` - направление движения (опционально)
- `limit int` - количество результатов (по умолчанию 50, макс. 100)
- `offset int` - смещение для пагинации

**Возвращает:**
- `[]EventInfo` - список событий
- `error` - ошибка поиска

#### `GetEventByID`

Получение события по ID вместе с фотографиями.

**Параметры:**
- `ctx context.Context` - контекст
- `eventID uuid.UUID` - ID события

**Возвращает:**
- `*EventInfo` - информация о событии с фотографиями
- `error` - ошибка (ErrNotFound если событие не найдено)

#### `GetEventsByPlateAndTime`

Получение событий по номеру и временному диапазону (для внутреннего использования).

**Параметры:**
- `ctx context.Context` - контекст
- `normalizedPlate string` - нормализованный номер
- `from time.Time` - начало временного диапазона
- `to time.Time` - конец временного диапазона
- `direction *string` - направление движения (опционально)

**Возвращает:**
- `[]EventInfo` - список событий (сортировка по времени ASC)
- `error` - ошибка поиска

#### `SyncVehicleToWhitelist`

Синхронизация номера транспортного средства в whitelist.

**Параметры:**
- `ctx context.Context` - контекст
- `plateNumber string` - номер транспортного средства

**Возвращает:**
- `uuid.UUID` - ID записи в `anpr_plates`
- `error` - ошибка синхронизации

**Логика:**
- Вызывает функцию БД `anpr_sync_vehicle_to_whitelist(plate_number)`
- Функция проверяет наличие номера в `vehicles` и синхронизирует с `anpr_plates`

#### `DeleteOldEvents`

Удаление событий старше указанного количества дней.

**Параметры:**
- `ctx context.Context` - контекст
- `days int` - количество дней (должно быть >= 1)

**Возвращает:**
- `int64` - количество удалённых событий
- `error` - ошибка удаления

#### `DeleteAllEvents`

Удаление всех событий из базы данных.

**Параметры:**
- `ctx context.Context` - контекст

**Возвращает:**
- `int64` - количество удалённых событий
- `error` - ошибка удаления

#### `CleanupOldEvents`

Автоматическая очистка старых событий (вызывается фоновой задачей).

**Параметры:**
- `ctx context.Context` - контекст
- `days int` - количество дней (по умолчанию 3)

**Возвращает:**
- `int64` - количество удалённых событий
- `error` - ошибка очистки

**Примечание:** Вызывается автоматически каждые 6 часов для удаления событий старше 3 дней.

---

## Нормализация номеров

Номер нормализуется функцией `utils.NormalizePlate()`:

1. Удаление всех пробелов
2. Удаление всех дефисов
3. Приведение к верхнему регистру
4. Удаление префикса страны (KZ, если есть)

**Примеры:**
- `"123 ABC 02"` → `"123ABC02"`
- `"KZ 123-ABC-02"` → `"123ABC02"`
- `"kz123abc02"` → `"123ABC02"`

---

## Автоматическая очистка старых событий

Сервис автоматически удаляет события старше 3 дней каждые 6 часов.

- Первая очистка выполняется через 1 минуту после запуска сервиса
- Последующие очистки - каждые 6 часов
- Удаляются события, у которых `created_at < (текущее_время - 3 дня)`
- Фотографии удаляются автоматически благодаря ON DELETE CASCADE

Логирование:
- Успешная очистка: `INFO` уровень с количеством удалённых событий
- Ошибка очистки: `ERROR` уровень с описанием ошибки

---

## Примеры использования

### Отправка события от камеры (JSON)

```bash
curl -X POST http://localhost:8082/api/v1/anpr/events \
  -H "Content-Type: application/json" \
  -d '{
    "camera_id": "camera-001",
    "plate": "123 ABC 02",
    "confidence": 0.95,
    "direction": "enter",
    "lane": 1,
    "event_time": "2025-01-21T12:34:56Z",
    "vehicle": {
      "color": "white",
      "type": "car"
    }
  }'
```

### Отправка события с фотографиями

```bash
curl -X POST http://localhost:8082/api/v1/anpr/events \
  -F "event={\"camera_id\":\"camera-001\",\"plate\":\"123 ABC 02\",\"event_time\":\"2025-01-21T12:34:56Z\"}" \
  -F "photos=@photo1.jpg" \
  -F "photos=@photo2.jpg"
```

### Поиск номеров (требует авторизацию)

```bash
curl -X GET "http://localhost:8082/api/v1/plates?plate=123ABC02" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### Поиск событий (требует авторизацию)

```bash
curl -X GET "http://localhost:8082/api/v1/events?plate=123ABC02&from=2025-01-01T00:00:00Z&to=2025-01-31T23:59:59Z&limit=50" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### Получение события по ID (требует авторизацию)

```bash
curl -X GET "http://localhost:8082/api/v1/events/550e8400-e29b-41d4-a716-446655440000" \
  -H "Authorization: Bearer <JWT_TOKEN>"
```

### Синхронизация транспорта (требует авторизацию)

```bash
curl -X POST http://localhost:8082/api/v1/anpr/sync-vehicle \
  -H "Authorization: Bearer <JWT_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "plate_number": "123 ABC 02"
  }'
```

### Удаление старых событий (требует авторизацию)

```bash
curl -X DELETE http://localhost:8082/api/v1/anpr/events/old \
  -H "Authorization: Bearer <JWT_TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{
    "days": 30
  }'
```

### Внутренний запрос событий (требует внутренний токен)

```bash
curl -X GET "http://localhost:8082/internal/anpr/events?plate=KZ123ABC02&start_time=2025-01-15T10:00:00Z&end_time=2025-01-15T18:00:00Z&direction=entry" \
  -H "X-Internal-Token: <INTERNAL_TOKEN>"
```

---

## Обработка ошибок

Сервис возвращает стандартные HTTP коды статуса:

- `200 OK` - успешный запрос
- `201 Created` - ресурс успешно создан
- `400 Bad Request` - невалидные данные запроса
- `401 Unauthorized` - отсутствует или невалидный токен авторизации
- `404 Not Found` - ресурс не найден
- `500 Internal Server Error` - внутренняя ошибка сервера
- `503 Service Unavailable` - сервис недоступен (health check)

Все ошибки возвращаются в формате:
```json
{
  "error": "описание ошибки"
}
```

---

## Логирование

Сервис использует `zerolog` для структурированного логирования.

**Уровни логирования:**
- `INFO` - информационные сообщения (обработка событий, успешные операции)
- `WARN` - предупреждения (R2 не настроен, фото не загружено)
- `ERROR` - ошибки (ошибки БД, обработки событий)
- `FATAL` - критические ошибки (не удалось подключиться к БД)

**Примеры логов:**
```
{"level":"info","plate":"123ABC02","camera_id":"camera-001","msg":"processing ANPR event"}
{"level":"info","event_id":"550e8400-...","plate_id":"660e8400-...","msg":"successfully processed and saved ANPR event"}
{"level":"warn","photos_count":2,"msg":"photos provided but R2 storage not configured, skipping photo upload"}
{"level":"error","error":"failed to create ANPR event","plate":"123ABC02","msg":"failed to process ANPR event"}
```

---

## Безопасность

1. **JWT авторизация** - все защищённые эндпоинты требуют валидный JWT токен
2. **Внутренний токен** - внутренние эндпоинты защищены отдельным токеном
3. **Валидация входных данных** - все входные данные валидируются перед обработкой
4. **Ограничение размера файлов** - максимальный размер фотографии 10MB, запроса 50MB
5. **Маскирование паролей** - пароли в RTSP URL маскируются в логах и ответах

---

## Производительность

- Автоматическая очистка старых событий каждые 6 часов
- Индексы на полях `normalized_plate`, `event_time`, `plate_id` для быстрого поиска
- Пагинация для больших списков событий (максимум 100 результатов за запрос)
- Асинхронная загрузка фотографий в R2 (не блокирует сохранение события)

---

## Расширение функциональности

### Добавление новых полей в событие

1. Добавить поле в `anpr.EventPayload` (`internal/domain/anpr/models.go`)
2. Добавить поле в `ANPREvent` (`internal/repository/anpr_repository.go`)
3. Обновить миграции БД (`internal/db/migrations.go`)
4. Обновить обработку в `ProcessIncomingEvent` (`internal/service/anpr_service.go`)

### Добавление новых фильтров поиска

1. Добавить параметр в `FindEvents` (`internal/service/anpr_service.go`)
2. Добавить фильтр в `FindEvents` репозитория (`internal/repository/anpr_repository.go`)
3. Добавить query параметр в handler (`internal/http/handler.go`)

---

## Troubleshooting

### События не сохраняются

- Проверьте подключение к БД (`DB_DSN`)
- Проверьте логи на наличие ошибок
- Убедитесь, что миграции выполнены

### Фотографии не загружаются

- Проверьте настройки R2 Storage в `app.env`
- Проверьте логи на наличие ошибок загрузки
- Убедитесь, что размер фотографий не превышает 10MB

### Камера не отправляет события

- Проверьте доступность эндпоинта `GET /api/v1/anpr/hikvision`
- Проверьте настройки webhook в камере
- Проверьте логи на наличие входящих запросов

### Ошибка авторизации

- Проверьте валидность JWT токена
- Убедитесь, что `JWT_ACCESS_SECRET` совпадает с секретом в auth-service
- Проверьте формат заголовка: `Authorization: Bearer <token>`

---

## Контакты и поддержка

Для вопросов и проблем обращайтесь к команде разработки SnowOps.
