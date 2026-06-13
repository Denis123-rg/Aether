package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/aether-arb/aether/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("aether-telebot: Telegram dashboard service starting")

	cfg, err := config.LoadProductionConfig(config.ProductionConfigPath())
	if err != nil {
		slog.Error("failed to load production config", "err", err)
		os.Exit(1)
	}

	token := cfg.Telegram.BotToken
	if token == "" {
		token = os.Getenv("TELEGRAM_BOT_TOKEN")
	}
	if token == "" {
		slog.Error("TELEGRAM_BOT_TOKEN not set")
		os.Exit(1)
	}

	adminIDs := cfg.Telegram.AdminChatIDs
	if len(adminIDs) == 0 {
		if raw := os.Getenv("TELEGRAM_ADMIN_CHAT_IDS"); raw != "" {
			adminIDs, err = config.ParseAdminChatIDs(raw)
			if err != nil {
				slog.Error("invalid TELEGRAM_ADMIN_CHAT_IDS", "err", err)
				os.Exit(1)
			}
		}
	}
	if len(adminIDs) == 0 {
		slog.Error("no admin chat IDs configured")
		os.Exit(1)
	}

	metricsURL := cfg.Telegram.ExecutorMetricsURL
	if metricsURL == "" {
		metricsURL = "http://localhost:8080/metrics/json"
	}

	redisURL := cfg.Redis.URL
	if redisURL == "" {
		redisURL = os.Getenv("REDIS_URL")
	}
	if redisURL == "" {
		config.RequireRedisInProduction()
	}

	botAPI, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		slog.Error("telegram bot init failed", "err", err)
		os.Exit(1)
	}
	botAPI.Debug = os.Getenv("TELEBOT_DEBUG") == "1"
	slog.Info("telegram bot authorized", "username", botAPI.Self.UserName)

	bot := NewTeleBot(
		botAPI,
		metricsURL,
		adminIDs,
		time.Duration(cfg.Telegram.DashboardUpdateIntervalSecs)*time.Second,
		redisURL,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutdown signal received")
		cancel()
	}()

	slog.Info("telebot running",
		"metrics_url", metricsURL,
		"poll_interval_secs", cfg.Telegram.DashboardUpdateIntervalSecs,
		"redis", redisURL != "",
		"admins", len(adminIDs),
	)
	bot.Run(ctx)
	slog.Info("telebot stopped")
}
