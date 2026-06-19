package main

import (
	"context"
	"fmt"
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutdown signal received")
		cancel()
	}()

	if err := start(ctx, newBotAPIFn); err != nil {
		slog.Error("telebot failed to start", "err", err)
		os.Exit(1)
	}
	slog.Info("telebot stopped")
}

var newBotAPIFn = func(token string) (BotAPI, error) {
	return tgbotapi.NewBotAPI(token)
}

func start(ctx context.Context, apiFactory func(string) (BotAPI, error)) error {
	cfg, err := config.LoadProductionConfig(config.ProductionConfigPath())
	if err != nil {
		return fmt.Errorf("failed to load production config: %w", err)
	}

	adminIDs := cfg.Telegram.AdminChatIDs
	metricsURL := cfg.Telegram.ExecutorMetricsURL

	redisURL := cfg.Redis.URL
	if redisURL == "" {
		redisURL = os.Getenv("REDIS_URL")
	}
	if redisURL == "" {
		config.RequireRedisInProduction()
	}

	botAPI, err := apiFactory(cfg.Telegram.BotToken)
	if err != nil {
		return fmt.Errorf("telegram bot init failed: %w", err)
	}

	botAPITyped, ok := botAPI.(*tgbotapi.BotAPI)
	if ok {
		botAPITyped.Debug = os.Getenv("TELEBOT_DEBUG") == "1"
		slog.Info("telegram bot authorized", "username", botAPITyped.Self.UserName)
	}

	bot := NewTeleBot(
		botAPI,
		metricsURL,
		adminIDs,
		time.Duration(cfg.Telegram.DashboardUpdateIntervalSecs)*time.Second,
		redisURL,
	)

	slog.Info("telebot running",
		"metrics_url", metricsURL,
		"poll_interval_secs", cfg.Telegram.DashboardUpdateIntervalSecs,
		"redis", redisURL != "",
		"admins", len(adminIDs),
	)
	bot.Run(ctx)
	return nil
}
