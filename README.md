# SnowOps ANPR Backend Service

Backend-сервис для работы с ANPR-камерой Hikvision (модель DS-TCG406-E), который принимает события распознавания гос. номеров и обрабатывает их.

## Возможности

- Приём webhook-событий от ANPR-камеры
- Парсинг и сохранение событий с распознанными номерами
- Нормализация гос. номеров
- Проверка номеров по whitelist/blacklist
- Поиск событий и номеров через REST API

## Технологии

- Go 1.22+
- GORM + PostgreSQL
- Gin (HTTP router)
- Zerolog (логирование)
- Viper (конфигурация)

## Структура проекта

```
snowops-anpr-service/
├── cmd/
│   └── anpr-service/
│       └── main.go
├── internal/
│   ├── auth/          # JWT парсер
│   ├── config/        # Конфигурация
│   ├── db/            # Подключение к БД и миграции
│   ├── domain/        # Доменные модели
│   ├── http/          # HTTP handlers и router
│   ├── logger/        # Логгер
│   ├── model/         # Общие модели
│   ├── repository/    # Репозитории для работы с БД
│   ├── service/       # Бизнес-логика
│   └── utils/         # Утилиты
├── Dockerfile
├── docker-compose.yml
├── app.env
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

## API Endpoints

### Health Checks

- `GET /health/live` - проверка работоспособности
- `GET /health/ready` - проверка готовности (включая БД)

### Авторизация

Большинство эндпоинтов требуют JWT токен в заголовке:
```
Authorization: Bearer <JWT_TOKEN>
```

**Публичные эндпоинты (без авторизации):**
- `POST /api/v1/anpr/events` - прием событий от камер
- `POST /api/v1/anpr/hikvision` - прием событий от Hikvision
- `GET /api/v1/anpr/hikvision` - проверка доступности камерой
- `GET /api/v1/camera/status` - статус камеры

**Защищенные эндпоинты (требуют авторизацию):**
- `GET /api/v1/plates` - поиск номеров
- `GET /api/v1/events` - список событий
- `GET /api/v1/events/:id` - событие с фото
- `POST /api/v1/anpr/sync-vehicle` - синхронизация транспорта
- `DELETE /api/v1/anpr/events/old` - удаление старых событий
- `DELETE /api/v1/anpr/events/all` - удаление всех событий

### ANPR Events

- `POST /api/v1/anpr/events` - приём события от камеры

Эндпоинт поддерживает два формата запроса:

#### 1. JSON (для обратной совместимости)

**Content-Type:** `application/json`

Пример запроса:

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
    "type": "car"
  },
  "snapshot_url": "http://camera/snapshot.jpg"
}
```

#### 2. Multipart Form Data (с фотографиями)

**Content-Type:** `multipart/form-data`

Поля формы:
- `event` (обязательно) - JSON строка с данными события
- `photos` (опционально) - файлы фотографий (можно передать несколько)

Пример запроса с фотографиями:

```bash
curl -X POST http://localhost:8082/api/v1/anpr/events \
  -F "event={\"camera_id\":\"camera-001\",\"plate\":\"123 ABC 02\",\"event_time\":\"2025-01-21T12:34:56Z\"}" \
  -F "photos=@photo1.jpg" \
  -F "photos=@photo2.jpg" \
  -F "photos=@photo3.jpg"
```

Фотографии загружаются в R2 и сохраняются в структуре: `anpr-events/{YYYY-MM-DD}/{HH-MM-SS}-{event_id}-{normalized_plate}/photo-{index}.{ext}`

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
    "https://cdn.example.com/anpr-photos/anpr-events/550e8400-.../photo-0.jpg",
    "https://cdn.example.com/anpr-photos/anpr-events/550e8400-.../photo-1.jpg"
  ]
}
```

**Примечания:**
- Если R2 не настроен, фотографии будут проигнорированы (событие всё равно сохранится)
- Максимальный размер одной фотографии: 10MB
- Максимальный размер всего запроса: 50MB
- Фотографии сохраняются в R2 с организацией по event_id для удобного управления

### Plates

- `GET /api/v1/plates?plate=123ABC02` - поиск номеров (требует авторизацию)

**Заголовки:**
```
Authorization: Bearer <JWT_TOKEN>
```

Ответ:

```json
{
  "data": [
    {
      "id": 45,
      "number": "123 ABC 02",
      "normalized": "123ABC02",
      "last_event_time": "2025-01-21T12:34:56Z"
    }
  ]
}
```

### Events

- `GET /api/v1/events?plate=123ABC02&from=2025-01-01T00:00:00Z&to=2025-01-31T23:59:59Z&limit=50&offset=0` - поиск событий (требует авторизацию)

**Заголовки:**
```
Authorization: Bearer <JWT_TOKEN>
```

**Параметры запроса:**
- `plate` (опционально) - номер машины для фильтрации
- `from` (опционально) - начало временного диапазона (RFC3339)
- `to` (опционально) - конец временного диапазона (RFC3339)
- `limit` (опционально) - количество результатов (по умолчанию 50, максимум 100)
- `offset` (опционально) - смещение для пагинации (по умолчанию 0)

**Пример ответа:**

```json
{
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "plate_id": "660e8400-e29b-41d4-a716-446655440001",
      "camera_id": "camera-001",
      "normalized_plate": "123ABC02",
      "raw_plate": "123 ABC 02",
      "event_time": "2025-01-21T12:34:56Z",
      "confidence": 0.95,
      "direction": "enter",
      "lane": 1,
      "vehicle_color": "white",
      "vehicle_type": "car"
    }
  ]
}
```

- `GET /api/v1/events/:id` - получение события по ID с фотографиями (требует авторизацию)

**Заголовки:**
```
Authorization: Bearer <JWT_TOKEN>
```

**Пример запроса:**
```
GET /api/v1/events/550e8400-e29b-41d4-a716-446655440000
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
    "photos": [
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-0.jpg",
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-1.jpg"
    ]
  }
}
```

**Примечания:**
- Если событие не найдено, возвращается статус 404
- Если ID невалидный, возвращается статус 400
- Если фото отсутствуют, поле `photos` будет пустым массивом
- Время в пути к фото использует часовой пояс Казахстана (GMT+5) (требует авторизацию)

**Заголовки:**
```
Authorization: Bearer <JWT_TOKEN>
```

**Параметры запроса:**
- `plate` (опционально) - номер машины для фильтрации
- `from` (опционально) - начало временного диапазона (RFC3339)
- `to` (опционально) - конец временного диапазона (RFC3339)
- `limit` (опционально) - количество результатов (по умолчанию 50, максимум 100)
- `offset` (опционально) - смещение для пагинации (по умолчанию 0)

Ответ:

```json
{
  "data": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440000",
      "plate_id": "660e8400-e29b-41d4-a716-446655440001",
      "camera_id": "camera-001",
      "normalized_plate": "123ABC02",
      "raw_plate": "123 ABC 02",
      "event_time": "2025-01-21T12:34:56Z",
      "confidence": 0.95,
      "direction": "enter",
      "lane": 1,
      "vehicle_color": "white",
      "vehicle_type": "car"
    }
  ]
}
```

- `GET /api/v1/events/:id` - получение события по ID с фотографиями (требует авторизацию)

**Заголовки:**
```
Authorization: Bearer <JWT_TOKEN>
```

**Пример запроса:**
```
GET /api/v1/events/550e8400-e29b-41d4-a716-446655440000
```

Ответ:

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
    "photos": [
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-0.jpg",
      "https://cdn.example.com/anpr-events/2025-01-21/12-34-56-550e8400-...-123ABC02/photo-1.jpg"
    ]
  }
}
```

**Примечания:**
- Если событие не найдено, возвращается статус 404
- Если ID невалидный, возвращается статус 400
- Если фото отсутствуют, поле `photos` будет пустым массивом

## База данных

Сервис создаёт следующие таблицы:

- `plates` - номера (исходный и нормализованный)
- `vehicles` - информация о ТС
- `anpr_events` - события распознавания
- `lists` - списки (whitelist/blacklist)
- `list_items` - элементы списков

Миграции выполняются автоматически при старте сервиса.

## Конфигурация

Все параметры настраиваются через переменные окружения (см. `.env.example`):

- `APP_ENV` - окружение (development/production)
- `HTTP_HOST` - хост для HTTP сервера
- `HTTP_PORT` - порт для HTTP сервера
- `DB_DSN` - строка подключения к PostgreSQL
- `JWT_ACCESS_SECRET` - секрет для JWT токенов
- `CAMERA_RTSP_URL` - RTSP URL камеры
- `CAMERA_HTTP_HOST` - HTTP хост камеры
- `CAMERA_MODEL` - модель камеры
- `HIK_CONNECT_DOMAIN` - домен HikConnect
- `ENABLE_SNOW_VOLUME_ANALYSIS` - включить анализ объёма снега

### R2 Storage (опционально, для загрузки фотографий)

- `R2_ENDPOINT` - R2 endpoint URL (например, `https://{account-id}.r2.cloudflarestorage.com`)
- `R2_ACCESS_KEY_ID` - Access Key ID для R2
- `R2_SECRET_ACCESS_KEY` - Secret Access Key для R2
- `R2_BUCKET` - название bucket в R2
- `R2_REGION` - регион (по умолчанию `auto`)
- `R2_PUBLIC_BASE_URL` - публичный URL для CDN (опционально, если используется CDN перед R2)

Если R2 не настроен, сервис будет работать без возможности загрузки фотографий.

