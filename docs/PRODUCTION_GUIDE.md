# Aether MEV Bot — Инструкция по запуску на Ethereum Mainnet

---

## Содержание

1. [Системные требования](#1-системные-требования)
2. [Установка зависимостей](#2-установка-зависимостей)
3. [Создание и настройка кошелька](#3-создание-и-настройка-кошелька)
4. [Полный .env файл для Mainnet](#4-полный-env-файл-для-mainnet)
5. [Локальный запуск (Linux/Mac)](#5-локальный-запуск-linuxmac)
6. [Установка на Ubuntu VPS](#6-установка-на-ubuntu-vps)
7. [Docker Compose (рекомендуемый способ)](#7-docker-compose-рекомендуемый-способ)
8. [Systemd-сервисы (без Docker)](#8-systemd-сервисы-без-docker)
9. [Наблюдаемость и мониторинг](#9-наблюдаемость-и-мониторинг)
10. [Переход в Live](#10-переход-в-live)
11. [Диагностика и troubleshooting](#11-диагностика-и-troubleshooting)

---

## 1. Системные требования

### Минимальные (только MEV-бэкраннинг + детекция)

| Компонент | Требование |
|-----------|-----------|
| CPU | 4 ядра (x86_64, Intel Xeon / AMD EPYC или современный ARM) |
| RAM | 8 GB |
| Диск | 50 GB SSD (NVMe рекомендуется) |
| Сеть | 100 Мбит/с, latency до RPC-провайдера < 100 мс |
| ОС | Ubuntu 22.04 / 24.04 LTS |

### Рекомендуемые (полный функционал: детекция + мемпул + симуляция)

| Компонент | Требование |
|-----------|-----------|
| CPU | 8+ ядер (x86_64, AVX-512 поддержка желательна) |
| RAM | 16–32 GB |
| Диск | 100 GB NVMe |
| Сеть | 1 Гбит/с, latency < 20 мс до RPC |
| ОС | Ubuntu 24.04 LTS |

### Для VPS (хостинг)

- **Hetzner CX52 (8 vCPU, 16 GB RAM)** — минимальный рекомендованный
- **Hetzner CX62 (16 vCPU, 32 GB RAM)** — для продакшена с мемпул-трекингом
- **OVH / AWS / GCP** — аналогично, с расположением в регионах EU (Frankfurt) или US-East (доступ к Alchemy/Flashbots с минимальной задержкой)

> ⚠️ **Важно:** Не используй shared hosting (OpenVZ, LXC). Нужен выделенный VPS (KVM) с Docker.

---

## 2. Установка зависимостей

### 2.1. Базовые пакеты (Ubuntu)

```bash
sudo apt update && sudo apt upgrade -y
sudo apt install -y \
  curl wget git build-essential pkg-config \
  libssl-dev libclang-dev \
  protobuf-compiler \
  docker.io docker-compose-v2 \
  tmux htop nvme-cli
```

### 2.2. Rust

```bash
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
source "$HOME/.cargo/env"
# Версия из rust-toolchain.toml установится автоматически при сборке
rustc --version  # должно быть 1.94+ или версия из файла
```

### 2.3. Go

```bash
wget https://go.dev/dl/go1.26.1.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc
source ~/.bashrc
go version  # go1.26.1
```

### 2.4. Foundry (для компиляции контрактов)

```bash
curl -L https://foundry.paradigm.xyz | bash
foundryup
forge --version
```

---

## 3. Создание и настройка кошелька

### 3.1. Создать новый searcher wallet

```bash
cast wallet new
```

Вывод должен быть примерно таким:
```
Successfully created new keypair.
Address:     0x1234...abcd
Private Key: 0x5678...ef01
Mnemonic:    ...
```

**Сохрани Private Key в SEARCHER_KEY в .env файле.**

### 3.2. Правила безопасности кошелька

- **НИКОГДА** не используй основной/холодный кошелек
- **НИКОГДА** не храни более 0.5 ETH на этом кошельке
- **НИКОГДА** не коммить .env в git (проверь .gitignore)
- **ВСЕГДА** ставь `chmod 600 .env`

### 3.3. Отправка ETH

Отправь ~0.5 ETH на полученный address для оплаты газа:
- Фактические затраты: ~0.01–0.05 ETH в день (зависит от активности)
- Рекомендуемый запас: 0.5 ETH (покрывает неделю работы + форс-мажор)

---

## 4. Полный .env файл для Mainnet

Файл `.env` должен лежать в корне проекта `/home/denis/Aether/.env`.
Права: `chmod 600 .env`.

```bash
# ═══════════════════════════════════════════════════════════════════════════
# AETHER MEV BOT — ПОЛНЫЙ MAINNET КОНФИГУРАЦИОННЫЙ ФАЙЛ
# ═══════════════════════════════════════════════════════════════════════════
# Копия: cp .env.example .env && chmod 600 .env
#
# ═══════════════════════════ ПРАВИЛА БЕЗОПАСНОСТИ ══════════════════════════
# 1. SEARCHER_KEY — ЭТО PRIVATE KEY ГОРЯЧЕГО КОШЕЛЬКА.
#    Создан командой: cast wallet new
#    На кошельке: НЕ БОЛЕЕ 0.5 ETH
#    Использование: только для подписи Flashbots-бандлов
#
# 2. .env НИКОГДА не должен попасть в git.
#    .gitignore уже содержит .env — проверь: git check-ignore .env
#
# 3. До перехода в LIVE (AETHER_SHADOW=0) обязательно проработай
#    минимум 24 часа в shadow mode (AETHER_SHADOW=1).
#
# 4. На VPS: файл .env должен быть доступен только пользователю aether:
#    chown aether:aether .env && chmod 600 .env
# ═══════════════════════════════════════════════════════════════════════════

# ──── 1. ETHEREUM RPC (ОБЯЗАТЕЛЬНО) ────────────────────────────────────────
# Alchemy (рекомендуется) — подписка на eth_pendingTransactions.
# Зарегистрируйся: https://www.alchemy.com
# Бесплатный тир: 300 CUPS, ~300k запросов/месяц.
# Для продакшена: Growth план ($49/мес) — 10M CU/мес, приоритетная поддержка.
ALCHEMY_API_KEY=ВАШ_ALCHEMY_API_KEY

# RPC URL для основного нода (форк ревма + чтение стейта)
ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}

# WebSocket для pending-транзакций (мемпул)
# Обязательно Alchemy — только они поддерживают alchemy_pendingTransactions
MEMPOOL_WS_URL=wss://eth-mainnet.g.alchemy.com/v2/${ALCHEMY_API_KEY}

# MEMPOOL_WS_URL по умолчанию = ws из ETH_RPC_URL.
# Можно не указывать, если ETH_RPC_URL — Alchemy.

# ──── 2. RUST CORE ─────────────────────────────────────────────────────────
RUST_LOG=info
AETHER_CONFIG_DIR=./config
AETHER_CHAIN_ID=1

# Пути к конфигурационным файлам (если нестандартные)
# AETHER_POOLS_CONFIG=./config/pools.toml
# AETHER_NODES_CONFIG=./config/nodes.yaml

# ──── 3. GO EXECUTOR ───────────────────────────────────────────────────────
GOMAXPROCS=2          # Не менять — 2 ядра оптимально для Go GC
GOGC=200              # Реже GC = больше аллокаций, но меньше latency
GRPC_ADDRESS=localhost:50051
METRICS_PORT=9090
DASHBOARD_PORT=8080

# ──── 4. СЕКРЕТНЫЙ КЛЮЧ КОШЕЛЬКА SEARCHER (ОБЯЗАТЕЛЬНО) ──────────────────
# Создан командой: cast wallet new
# На кошельке ~0.5 ETH
# НИКОГДА не используй основной кошелек!
SEARCHER_KEY=0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef

# ──── 5. FLASHBOTS AUTH (альтернатива SEARCHER_KEY) ────────────────────────
# Используется, если у тебя есть отдельный Flashbots API ключ.
# Если не знаешь — оставь закомментированным, используется SEARCHER_KEY.
# FLASHBOTS_AUTH_KEY=

# ──── 6. КОНТРАКТЫ И СИМУЛЯЦИЯ ─────────────────────────────────────────────
# Адрес AetherExecutor контракта.
# В SHADOW MODE: 0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45 (UniV3 SwapRouter02 — placeholder)
# В LIVE: замени на адрес твоего развернутого контракта.
AETHER_EXECUTOR_ADDRESS=0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45

# Caller для executeArb (обязательно EOA без кода, не равен executor_address)
AETHER_SEARCHER_CALLER=0x000000000000000000000000000000000000dEaD

# WETH адрес (для расчетов)
AETHER_PROFIT_TOKEN=0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2

# ──── 7. МЕМПУЛ-БЭКРАННИНГ ─────────────────────────────────────────────────
MEMPOOL_TRACKING=1              # Включить мемпул-трекинг (Alchemy)
MEMPOOL_POST_STATE_REPLAY=1     # Пост-стейт реплей для tick-crossing
# MEMPOOL_TRACKING=0            # = только блок-драйвен детекция

# Минимальная прибыль (wei) для мемпул-бандла. 1e14 = 0.0001 ETH.
# Фильтрует пыль, но пропускает реальные арбы.
AETHER_MEMPOOL_MIN_PROFIT_WEI=100000000000000

# ──── 8. RPC TRANSPORT (hardening от 429 ошибок) ─────────────────────────
# Alchemy CUPS (вычислительных единиц в секунду).
# Бесплатный тир: 300, Growth: до 10к+
AETHER_RPC_CUPS=300
AETHER_RPC_MAX_RETRIES=10
AETHER_RPC_BACKOFF_MS=200
AETHER_RPC_REQUEST_TIMEOUT_MS=10000   # 10 секунд на запрос

# ──── 9. МЕМПУЛ-СИМУЛЯЦИЯ ──────────────────────────────────────────────────
AETHER_MEMPOOL_SIM_RETRIES=1          # 1 повтор при транспортной ошибке
AETHER_MEMPOOL_SIM_TIMEOUT_MS=30000   # 30 секунд на симуляцию
AETHER_MEMPOOL_SIM_CONCURRENCY=4      # 4 параллельные симуляции
AETHER_BOOT_FETCH_CONCURRENCY=3       # 3 параллельных запроса при загрузке
AETHER_PREWARM_CONCURRENCY=4          # 4 параллельных prewarm-запроса

# ──── 10. РИСК-МЕНЕДЖМЕНТ ──────────────────────────────────────────────────
# Максимальная цена газа (Gwei) — бот не будет отправлять выше
# AETHER_MAX_GAS_PRICE_GWEI=300

# ──── 11. ТЕЛЕГРАМ-ОПОВЕЩЕНИЯ (опционально) ───────────────────────────────
# Создай бота через @BotFather, получи токен.
# TELEGRAM_BOT_TOKEN=
# TELEGRAM_CHAT_ID=

# ──── 12. SLACK ДЛЯ АЛЕРТОВ (опционально) ──────────────────────────────────
# SLACK_WEBHOOK_URL=

# ──── 13. LEDGER (POSTGRES) ────────────────────────────────────────────────
# Включает запись всех арбов/бандлов/инклюженов в базу.
# Используй только если разворачиваешь Postgres.
# DATABASE_URL=postgres://aether:aether@localhost:5432/aether
# POSTGRES_USER=aether
# POSTGRES_PASSWORD=aether
# POSTGRES_DB=aether

# ──── 14. TELEMETRY (OpenTelemetry ─────────────────────────────────────────
# OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317
# OTEL_SERVICE_NAME=aether-executor

# ──── 15. ЛОГИРОВАНИЕ ─────────────────────────────────────────────────────
# LOG_FORMAT=json        # Структурированные JSON-логи для Loki/Promtail
```

---

## 5. Локальный запуск (Linux/Mac)

### 5.1. Клонирование и подготовка

```bash
# Уже есть папка /home/denis/Aether, не клонируем.

cd /home/denis/Aether

# Настройка .env
cp .env.example .env
chmod 600 .env
nano .env    # заполни ALCHEMY_API_KEY и SEARCHER_KEY

# Компиляция контракта (обязательно!)
cd contracts && forge build && cd ..

# Компиляция Rust core
cargo build --release -p aether-grpc-server
# Бинарник: ./target/release/aether-rust

# Компиляция Go executor
go build -o aether-executor ./cmd/executor/
```

### 5.2. Запуск Rust core (терминал 1)

```bash
cd /home/denis/Aether

export AETHER_CONFIG_DIR=./config
export ETH_RPC_URL="https://eth-mainnet.g.alchemy.com/v2/ВАШ_КЛЮЧ"
export AETHER_NODES_CONFIG=./config/nodes.yaml
export RUST_LOG=info
export AETHER_CHAIN_ID=1

./target/release/aether-rust

# Признаки успешного запуска:
# 1. INFO aether_grpc_server: listening on [::]:50051
# 2. INFO pools: loaded X pools from config
# 3. INFO discovery: loaded X tokens from cache
# 4. Метрики: curl http://localhost:9092/metrics
```

### 5.3. Запуск Go executor (терминал 2)

```bash
cd /home/denis/Aether

# SHADOW MODE (безопасно, бандлы не отправляются)
export AETHER_SHADOW=1

export AETHER_CONFIG_DIR=./config
export ETH_RPC_URL="https://eth-mainnet.g.alchemy.com/v2/ВАШ_КЛЮЧ"
export GRPC_ADDRESS=localhost:50051
export SEARCHER_KEY="0x...ваш_ключ"

# Проверка загрузки конфига билдеров
export AETHER_EXECUTOR_ADDRESS=0x68b3465833fb72A70ecDF485E0e4C7bD8665Fc45

./aether-executor

# Признаки успешного запуска:
# 1. INFO builders loaded count=5 routing_mode=fanout
# 2. INFO executor config loaded executor_address=0x...
# 3. INFO connected to ethereum node
# 4. INFO chain ID verified chain_id=1
```

### 5.4. Проверка работы

```bash
# Метрики executor
curl http://localhost:9090/metrics | head -30

# Health check
curl http://localhost:8080/health

# Логи Rust core
curl http://localhost:9092/metrics | grep -E "blocks_processed|pools_loaded"
```

---

## 6. Установка на Ubuntu VPS

### 6.1. Создание пользователя

```bash
sudo adduser aether --disabled-password
sudo usermod -aG docker aether
sudo -u aether -i
```

### 6.2. Клонирование и настройка

```bash
sudo -u aether -i
cd ~

# Скопируй проект (или через rsync/scp с локальной машины)
git clone https://github.com/aether-arb/aether.git
cd aether

# Настройка .env
cp .env.example .env
chmod 600 .env
nano .env   # заполни ALCHEMY_API_KEY, SEARCHER_KEY
```

### 6.3. Установка зависимостей (VPS)

```bash
# Rust
curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y
source "$HOME/.cargo/env"

# Go
wget https://go.dev/dl/go1.26.1.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.26.1.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' >> ~/.bashrc

# Foundry
curl -L https://foundry.paradigm.xyz | bash
source ~/.bashrc
foundryup

# Компиляция контракта
cd contracts && forge build && cd ..

# Компиляция Rust
cargo build --release -p aether-grpc-server

# Компиляция Go executor
go build -o aether-executor ./cmd/executor/
```

### 6.4. Компиляция с оптимизациями

```bash
# Для максимальной производительности
export RUSTFLAGS="-C target-cpu=native"
cargo build --release -p aether-grpc-server

# Если не хватает RAM на компиляцию:
# RUSTFLAGS="" cargo build --release -p aether-grpc-server
```

---

## 7. Docker Compose (рекомендуемый способ)

### 7.1. Запуск полного стека

```bash
cd /home/denis/Aether/deploy/docker

# Настройка .env для Docker
cp .env.example .env
chmod 600 .env
nano .env   # заполни ETH_RPC_URL, SEARCHER_KEY

# Компиляция контракта (обязательно для Docker-сборки!)
cd ../../contracts && forge build && cd ../deploy/docker

# Запуск (SHADOW MODE по умолчанию)
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build

# Проверка
docker compose ps
docker compose logs -f aether-rust aether-go
```

### 7.2. Просмотр логов

```bash
# Все логи
docker compose logs -f

# Только app-сервисы
docker compose logs -f aether-rust aether-go

# Только мониторинг
docker compose logs -f prometheus grafana
```

### 7.3. Сервисы в стеке

| Сервис | Порт | Описание |
|--------|------|----------|
| aether-rust | 50051 (gRPC), 9092 (metrics) | Rust core engine |
| aether-go | 9090 (metrics), 8080 (dashboard) | Go executor |
| prometheus | 9091 | Метрики |
| alertmanager | 9093 | Алерты |
| grafana | 3001 | Дашборды |
| loki | 3100 | Логи |
| promtail | — | Сборщик логов |
| tempo | 3200, 4317 | Трейсинг (OTLP) |
| canary | — | Проверка здоровья |

### 7.4. Доступ к дашбордам

| URL | Логин | Пароль |
|-----|-------|--------|
| http://localhost:3001 | admin | admin |
| http://localhost:9091 | — | — |
| http://localhost:3100 | — | — |

### 7.5. Команды управления Docker

```bash
# Остановка
docker compose down

# Остановка с удалением данных
docker compose down -v

# Перезапуск одного сервиса
docker compose restart aether-rust

# Обновление (после изменений в коде)
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build

# Логи в реальном времени
docker compose logs -f --tail=100 aether-rust aether-go
```

---

## 8. Systemd-сервисы (без Docker)

### 8.1. Rust core сервис

```bash
sudo nano /etc/systemd/system/aether-rust.service
```

```ini
[Unit]
Description=Aether Rust Core
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=aether
Group=aether
WorkingDirectory=/home/aether/aether

EnvironmentFile=/home/aether/aether/.env
Environment=AETHER_CONFIG_DIR=/home/aether/aether/config
Environment=RUST_LOG=info

ExecStart=/home/aether/aether/target/release/aether-rust
Restart=always
RestartSec=10

# Безопасность
ProtectSystem=full
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

### 8.2. Go executor сервис

```bash
sudo nano /etc/systemd/system/aether-executor.service
```

```ini
[Unit]
Description=Aether Go Executor
After=aether-rust.service
Requires=aether-rust.service

[Service]
Type=simple
User=aether
Group=aether
WorkingDirectory=/home/aether/aether

EnvironmentFile=/home/aether/aether/.env
Environment=AETHER_CONFIG_DIR=/home/aether/aether/config
Environment=AETHER_SHADOW=1   # ← сначала shadow, потом 0
Environment=GRPC_ADDRESS=localhost:50051

ExecStart=/home/aether/aether/aether-executor
Restart=always
RestartSec=10

# Безопасность
ProtectSystem=full
NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
```

### 8.3. Активация сервисов

```bash
sudo systemctl daemon-reload
sudo systemctl enable aether-rust aether-executor
sudo systemctl start aether-rust

# Проверка статуса
sudo systemctl status aether-rust
sudo journalctl -u aether-rust -f

# После проверки Rust core
sudo systemctl start aether-executor
sudo journalctl -u aether-executor -f
```

---

## 9. Наблюдаемость и мониторинг

### 9.1. Ключевые метрики (Prometheus)

```bash
# Rust core
curl http://localhost:9092/metrics | grep -E "^aether_"

# Go executor
curl http://localhost:9090/metrics | grep -E "^aether_"
```

### 9.2. Важные метрики для отслеживания

| Метрика | Описание | Норма |
|---------|----------|-------|
| `aether_pools_loaded` | Количество загруженных пулов | > 2000 |
| `aether_blocks_processed` | Блоков обработано | > 6000/час |
| `aether_arbs_found_total` | Найдено арбитражей | > 0 |
| `aether_arbs_submitted_total` | Отправлено бандлов | > 0 |
| `aether_builder_accepted_total` | Бандлов принято билдерами | > 50% от отправленных |
| `aether_system_state` | Состояние бота | 0=running |
| `aether_eth_balance` | Баланс ETH | > 0.1 |
| `aether_bundle_latency_ms` | Задержка отправки | < 2000 ms |

### 9.3. Alerts (что должно быть настроено)

| Alert | Условие | Действие |
|-------|---------|----------|
| BuilderDown | builder не принимал бандлы > 5 минут | Slack / Telegram |
| SystemHalted | system_state = 3 | Срочная проверка |
| LowBalance | balance < 0.1 ETH | Пополнить кошелек |
| HighRevertRate | > 90% reverts за час | Проверить симуляцию |
| BundleMissRate | > 80% miss за час | Проверить RPC/билдеров |

---

## 10. Переход в Live

### 10.1. Чеклист перед Live

- [ ] Бот проработал в SHADOW MODE **минимум 24 часа**
- [ ] Shadow-дампы в `reports/bundles/` показывают корректные бандлы
- [ ] Процент успешных симуляций > 50%
- [ ] Все 5 билдеров отвечают (проверить метрики)
- [ ] RPC (Alchemy) стабилен, нет 429 ошибок
- [ ] ETH баланс > 0.3 ETH (после газа останется > 0.1)
- [ ] Searcher wallet создан отдельно, на нем <= 0.5 ETH
- [ ] Slack/TG алерты настроены

### 10.2. Включение Live режима

```bash
# Выход из shadow mode
export AETHER_SHADOW=0

# При Docker:
# Отредактируй deploy/docker/.env:
#   AETHER_SHADOW=0
# Затем:
cd deploy/docker
docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d --build

# Без Docker:
sudo systemctl stop aether-executor
# Отредактируй /etc/systemd/system/aether-executor.service:
#   Environment=AETHER_SHADOW=0
sudo systemctl daemon-reload
sudo systemctl start aether-executor
```

### 10.3. Первые минуты Live

```bash
# Мониторинг в реальном времени
docker compose logs -f --tail=50 aether-rust aether-go

# Метрики
watch -n 5 'curl -s http://localhost:9090/metrics | grep -E "aether_arbs_submitted|aether_builder_accepted"'

# Health check
watch -n 10 'curl -s http://localhost:8080/health | jq .'
```

---

## 11. Диагностика и troubleshooting

### 11.1. Executor не стартует

| Ошибка | Причина | Решение |
|--------|---------|---------|
| `eth_getCode returned empty` | AETHER_EXECUTOR_ADDRESS не имеет кода | В shadow: используй 0x68b3... SwapRouter02. В live: разверни контракт |
| `chain ID mismatch` | expected_chain_id != eth_chainId | Проверь config/executor.yaml. Mainnet=1 |
| `no signer configured` | SEARCHER_KEY не установлен | Заполни SEARCHER_KEY в .env |
| `builder reverted` | Бандл не прошел симуляцию | Проверь calldata, адреса, ликвидность пулов |

### 11.2. Нет арбитражей

| Причина | Проверка |
|---------|----------|
| Пул не загружен | `aether_pools_loaded` метрика = 0 |
| RPC не отвечает | `eth_blockNumber` вручную |
| Слишком высокий min profit | Уменьши AETHER_MEMPOOL_MIN_PROFIT_WEI |
| Цепь не mainnet | `cast chain-id` должно быть 1 |
| Всё ок, но нет профита | Проверь shadow-дампы: может арбы есть, но ниже порога |

### 11.3. 429 Rate Limited (Alchemy)

```bash
# Увеличь CUPS в .env
AETHER_RPC_CUPS=600    # для Growth плана
AETHER_RPC_CUPS=1500   # для Scale плана

# Или уменьши конкурентность
AETHER_MEMPOOL_SIM_CONCURRENCY=2
AETHER_BOOT_FETCH_CONCURRENCY=2
AETHER_PREWARM_CONCURRENCY=2
```

### 11.4. Бандлы не включаются в блок

| Причина | Что делать |
|---------|-----------|
| Слишком низкий tip | Проверь tip_share_pct в risk.yaml (min 50%) |
| Билдер не принял | Проверь метрику `aether_builder_accepted_total` по билдерам |
| RPC latency | Используй Alchemy Growth или Scale |
| Бандл пришел поздно | Увеличь timeout_ms (не помогло = проблема в RPC) |

### 11.5. Проверка билдеров вручную

```bash
# Проверка каждого билдера отдельно
for builder in \
  "https://relay.flashbots.net" \
  "https://rpc.titanbuilder.xyz" \
  "https://rpc.buildernet.org" \
  "https://rpc.quasar.win" \
  "https://rsync-builder.xyz"
do
  echo "=== $builder ==="
  curl -s -X POST -H "Content-Type: application/json" \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "$builder" | jq .result
done
```

Должен вернуться текущий номер блока (hex) — значит билдер жив.

---

> **Последнее обновление:** Июнь 2026
> **Ethereum Mainnet** | Aether MEV Bot v0.1
