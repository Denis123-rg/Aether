# Production Deploy — Ethereum Mainnet

## 1. Предварительные требования

- VPS: 4+ CPU, 8GB+ RAM, 50GB+ SSD, Ubuntu 22.04+
- Docker + Docker Compose v2
- Git
- Alchemy API key (или другой провайдер с `eth_pendingTransactions`)
- MetaMask / cast wallet для деплоя `AetherExecutor.sol`
- Searcher EOA (новый кошелек, ~0.5 ETH)

## 2. Клонирование и подготовка

```bash
git clone <repo-url> /opt/aether
cd /opt/aether
```

## 3. Build артефактов

```bash
# 3.1 Solidity контракты
cd contracts && forge build && cd ..

# 3.2 Rust core
cargo build --release --bin aether-rust

# 3.3 Go binaries
go build -o bin/aether-executor ./cmd/executor
go build -o bin/aether-monitor ./cmd/monitor
```

Либо используйте Docker (рекомендуется):

```bash
# сборка через Docker Compose
cd deploy/docker
docker compose -f docker-compose.yml -f docker-compose.prod.yml build
```

## 4. Заполнить .env

```bash
cp .env.example .env && chmod 600 .env
cp deploy/docker/.env.example deploy/docker/.env && chmod 600 deploy/docker/.env
```

### Обязательные переменные:

| Переменная | Описание | Откуда взять |
|---|---|---|
| `ALCHEMY_API_KEY` | Alchemy API key | alchemy.com |
| `ETH_RPC_URL` | Mainnet RPC (Alchemy) | `https://eth-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}` |
| `MEMPOOL_WS_URL` | WebSocket для mempool | `wss://eth-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}` |
| `SEARCHER_KEY` | Приватный ключ hot-wallet | `cast wallet new` |
| `AETHER_ADMIN_TOKEN` | Токен для админ-эндпоинтов | `openssl rand -hex 32` |
| `AETHER_EXECUTOR_ADDRESS` | Адрес задеплоенного контракта | После п.5 |

## 5. Деплой контракта AetherExecutor.sol

```bash
cd contracts
forge script script/Deploy.s.sol \
  --rpc-url $ETH_RPC_URL \
  --private-key $DEPLOYER_KEY \
  --broadcast \
  --verify
```

> Сохраните задеплоенный адрес, установите его в `AETHER_EXECUTOR_ADDRESS` в `.env`.

## 6. Режимы запуска

### 6.1 Shadow Mode (безопасный, первые 24 часа)

`.env`:
```
AETHER_SHADOW=1
AETHER_BACKRUN_MODE=shadow_only
```

Бот симулирует бандлы, НЕ отправляет их на реальные исполнение. Проверяет:
- Коннект к RPC и подписка на mempool
- Декодинг pending транзакций
- Fork-симуляция backrun
- Discovery: находит новые пулы, обновляет hot cache (top 500, 5 сек)
- Состояние системы: `Running`

### 6.2 Shadow + Live (переходный режим)

```env
AETHER_SHADOW=0
AETHER_BACKRUN_MODE=shadow_and_live
```

Бот симулирует И отправляет реальные бандлы. Рекомендуется:
- Следить за метриками (Grafana dashboard)
- Проверить включение бандлов в блоки
- Проверить P&L (нет убыточных операций)

### 6.3 Full Live (полный продакшен)

```env
AETHER_SHADOW=0
AETHER_BACKRUN_MODE=live_only
```

## 7. Discovery — 500+ пулов

Уже настроено в `config/discovery.toml`:
- `top_n = 500` — hot cache хранит топ-500 пулов
- `update_interval_secs = 5` — обновление каждые 5 секунд
- 8 DEX фабрик на прослушке (UniV2, UniV3, SushiSwap, Curve, Balancer V2/V3, Bancor V3)
- Новые пулы проходят: валидацию → скоринг (TVL * volume) → hot cache
- Старые пулы удаляются через `prune_interval_secs = 3600`

## 8. Mempool Backrun

Уже настроено:
- `MEMPOOL_TRACKING=1` — подписка на pending транзакции Alchemy
- `MEMPOOL_POST_STATE_REPLAY=1` — симуляция post-state для V3/Balancer
- Фильтрация по DEX router адресам (только свопы)
- Fork-симуляция через revm (спецификация Cancun)
- Декодинг calldata для UniV2, UniV3, SushiSwap, Curve, Balancer

## 9. Запуск Docker Compose (рекомендуемый способ)

```bash
cd deploy/docker
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

Сервисы:
| Сервис | Порт | Описание |
|---|---|---|
| aether-rust | 50051 (gRPC) | Rust core (детектор, симулятор, mempool) |
| aether-go | 8080 (admin), 9090 (metrics) | Go executor (бандлы, билдеры) |
| prometheus | 9091 | Метрики |
| grafana | 3001 | Дашборды |
| loki + promtail | 3100 | Логи |
| tempo | 4317 (OTLP) | Трейсинг |
| alertmanager | 9093 | Алерты |
| canary | - | Проверка здоровья |
| postgres * | 5432 | Trade ledger (profile: ledger) |

## 10. Запуск через systemd (альтернатива)

```bash
# Создать пользователя
sudo useradd -r -s /bin/false aether

# Скопировать бинарники
mkdir -p /opt/aether/bin /opt/aether/config /opt/aether/data /opt/aether/logs
cp target/release/aether-rust /opt/aether/bin/
cp bin/aether-executor /opt/aether/bin/

# Скопировать конфиги
cp -r config/* /opt/aether/config/
cp .env /opt/aether/.env

# Установить systemd сервисы
sudo cp deploy/systemd/*.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable aether-rust aether-go
sudo systemctl start aether-rust aether-go
```

## 11. Проверка работоспособности

```bash
# Статус контейнеров
docker compose ps

# Логи
docker compose logs -f aether-rust aether-go

# Метрики
curl http://localhost:9090/metrics | head -20

# gRPC health check (требуется grpcurl)
grpcurl -plaintext localhost:50051 aether.HealthService/Check
```

## 12. Важные метрики для мониторинга

| Метрика | Что означает | Целевое значение |
|---|---|---|
| `aether_pending_arb_candidates_total` | Кандидаты из mempool | > 0 |
| `aether_mempool_backrun_rejected_total` | Отклоненные backrun | < 50% |
| `aether_detector_cycles_found_total` | Найденные арбитражные циклы | > 0 |
| `aether_bundles_submitted_total` | Отправленные бандлы | > 0 |
| `aether_bundles_included_total` | Включенные в блоки | > 0% |
| `aether_eth_balance` | Баланс searcher wallet | >= 0.1 ETH |

## 13. Безопасность

- `.env` — `chmod 600`, никогда в git
- Searcher wallet — отдельный кошелек с ~0.5 ETH
- `AETHER_ADMIN_TOKEN` — сложный токен (32 байта hex)
- Telegram bot token — создать через @BotFather
- Postgres пароль — сменить `change-me`
- Grafana пароль — сменить `change-me`
- В production используйте `cmd/signer` для управления ключами

## 14. Rollback

```bash
# Docker
cd deploy/docker
docker compose -f docker-compose.yml -f docker-compose.prod.yml down
git checkout <previous-tag>
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build

# Systemd
sudo systemctl stop aether-go aether-rust
# переключить симлинк на предыдущую версию
sudo ln -sfn /opt/aether/releases/previous /opt/aether/current
sudo systemctl start aether-rust aether-go
```
