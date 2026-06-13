package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"github.com/aether-arb/aether/internal/events"
	"github.com/aether-arb/aether/internal/metrics"
)

// BotAPI is the subset of telegram.BotAPI used by TeleBot (mockable in tests).
type BotAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	GetUpdatesChan(config tgbotapi.UpdateConfig) tgbotapi.UpdatesChannel
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
}

// TeleBot is the Telegram dashboard bot.
type TeleBot struct {
	api            BotAPI
	metricsClient  *MetricsClient
	adminClient    *AdminClient
	adminChatIDs   map[int64]struct{}
	pollInterval   time.Duration
	redisState     *events.DashboardState
	redisSub       *events.Subscriber
	redisActive    bool

	mu              sync.Mutex
	dashboardMsgID  map[int64]int // chatID → messageID for edit-in-place
	refreshCh       chan struct{}
	resetPending    map[int64]bool
}

// NewTeleBot creates a configured telebot instance.
func NewTeleBot(
	api BotAPI,
	metricsURL string,
	adminChatIDs []int64,
	pollInterval time.Duration,
	redisURL string,
) *TeleBot {
	admins := make(map[int64]struct{}, len(adminChatIDs))
	for _, id := range adminChatIDs {
		admins[id] = struct{}{}
	}
	if pollInterval <= 0 {
		pollInterval = 3 * time.Second
	}

	state := &events.DashboardState{SignerHealthy: true}
	bot := &TeleBot{
		api:           api,
		metricsClient: NewMetricsClient(metricsURL),
		adminClient:   NewAdminClient(metricsURL),
		adminChatIDs:  admins,
		pollInterval:  pollInterval,
		redisState:    state,
		dashboardMsgID: make(map[int64]int),
		refreshCh:     make(chan struct{}, 1),
		resetPending:  make(map[int64]bool),
	}

	sub := events.NewSubscriber(redisURL, state, bot.triggerRefresh)
	if sub != nil {
		bot.redisSub = sub
		bot.redisActive = true
		slog.Info("redis subscriber active — event-driven dashboard refresh")
	} else {
		slog.Info("redis unavailable — falling back to polling-only mode")
	}
	return bot
}

func (b *TeleBot) triggerRefresh() {
	select {
	case b.refreshCh <- struct{}{}:
	default:
	}
}

// Run starts command handling and dashboard polling loops.
func (b *TeleBot) Run(ctx context.Context) {
	if b.redisSub != nil {
		b.redisSub.Start(ctx)
		defer b.redisSub.Stop()
	}

	go b.pollLoop(ctx)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return
		case <-b.refreshCh:
			b.refreshAllDashboards(ctx)
		case update := <-updates:
			b.handleUpdate(ctx, update)
		}
	}
}

func (b *TeleBot) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(b.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.refreshAllDashboards(ctx)
		}
	}
}

func (b *TeleBot) refreshAllDashboards(ctx context.Context) {
	for chatID := range b.adminChatIDs {
		b.mu.Lock()
		msgID, hasDashboard := b.dashboardMsgID[chatID]
		b.mu.Unlock()
		if hasDashboard {
			b.updateDashboard(ctx, chatID, msgID)
		}
	}
}

func (b *TeleBot) handleUpdate(ctx context.Context, update tgbotapi.Update) {
	if update.Message == nil || !update.Message.IsCommand() {
		if update.CallbackQuery != nil {
			b.handleCallback(ctx, update.CallbackQuery)
		}
		return
	}
	chatID := update.Message.Chat.ID
	if !b.isAdmin(chatID) {
		return
	}

	cmd := update.Message.Command()
	args := update.Message.CommandArguments()

	switch cmd {
	case "start", "help":
		b.sendHelp(chatID)
	case "dashboard":
		b.sendDashboard(ctx, chatID)
	case "pools":
		b.sendPools(ctx, chatID)
	case "pause":
		b.handlePause(ctx, chatID)
	case "resume":
		b.handleResume(ctx, chatID)
	case "reset":
		b.handleReset(ctx, chatID)
	case "reset_confirm":
		b.handleResetConfirm(ctx, chatID)
	case "set_min_profit":
		b.handleSetMinProfit(ctx, chatID, args)
	case "health":
		b.sendHealth(ctx, chatID)
	case "trades":
		b.sendTrades(ctx, chatID)
	default:
		b.reply(chatID, "Unknown command. Try /dashboard or /help")
	}
}

func (b *TeleBot) handleCallback(ctx context.Context, cb *tgbotapi.CallbackQuery) {
	if cb.Message == nil || !b.isAdmin(cb.Message.Chat.ID) {
		return
	}
	chatID := cb.Message.Chat.ID
	switch cb.Data {
	case "refresh":
		b.updateDashboard(ctx, chatID, cb.Message.MessageID)
	case "pools":
		b.sendPools(ctx, chatID)
	case "health":
		b.sendHealth(ctx, chatID)
	case "trades":
		b.sendTrades(ctx, chatID)
	case "pause":
		b.handlePause(ctx, chatID)
	case "resume":
		b.handleResume(ctx, chatID)
	}
	callback := tgbotapi.NewCallback(cb.ID, "")
	_, _ = b.api.Request(callback)
}

func (b *TeleBot) isAdmin(chatID int64) bool {
	_, ok := b.adminChatIDs[chatID]
	return ok
}

func (b *TeleBot) sendHelp(chatID int64) {
	text := strings.TrimSpace(`
🤖 *Aether TeleBot Commands*

/dashboard — Live metrics dashboard (auto-refreshes)
/pools — Top 20 hot pools by score
/pause — Pause bundle submission
/resume — Resume bundle submission
/set_min_profit <eth> — Adjust min profit threshold
/health — Component health status
/trades — Last 10 trades
`)
	b.reply(chatID, text)
}

func (b *TeleBot) sendDashboard(ctx context.Context, chatID int64) {
	snap, err := b.fetchSnapshot(ctx)
	text := FormatDashboard(snap, b.redisState.Get(), b.redisActive)
	if err != nil {
		text = FormatDashboard(metrics.Snapshot{ExecutorReachable: false}, b.redisState.Get(), b.redisActive)
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	msg.ReplyMarkup = dashboardKeyboard()
	sent, err := b.api.Send(msg)
	if err != nil {
		slog.Error("send dashboard failed", "err", err)
		return
	}
	b.mu.Lock()
	b.dashboardMsgID[chatID] = sent.MessageID
	b.mu.Unlock()
}

func (b *TeleBot) updateDashboard(ctx context.Context, chatID int64, msgID int) {
	snap, err := b.fetchSnapshot(ctx)
	text := FormatDashboard(snap, b.redisState.Get(), b.redisActive)
	if err != nil {
		text = FormatDashboard(metrics.Snapshot{ExecutorReachable: false}, b.redisState.Get(), b.redisActive)
	}

	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "Markdown"
	kbd := dashboardKeyboard()
	edit.ReplyMarkup = &kbd
	if _, err := b.api.Send(edit); err != nil {
		slog.Debug("edit dashboard failed", "err", err)
	}
}

func (b *TeleBot) sendPools(ctx context.Context, chatID int64) {
	snap, err := b.fetchSnapshot(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Executor unreachable — cannot fetch pools")
		return
	}
	b.reply(chatID, FormatPools(snap.TopPools))
}

func (b *TeleBot) sendHealth(ctx context.Context, chatID int64) {
	snap, err := b.fetchSnapshot(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Executor unreachable")
		return
	}
	b.reply(chatID, FormatHealth(snap))
}

func (b *TeleBot) sendTrades(ctx context.Context, chatID int64) {
	snap, err := b.fetchSnapshot(ctx)
	if err != nil {
		b.reply(chatID, "⚠️ Executor unreachable")
		return
	}
	b.reply(chatID, FormatTrades(snap.RecentTrades))
}

func (b *TeleBot) handlePause(ctx context.Context, chatID int64) {
	if err := b.adminClient.Pause(ctx); err != nil {
		b.reply(chatID, formatAdminError("Pause", err))
		return
	}
	b.reply(chatID, "⏸ Bundle submission *paused*")
	b.triggerRefresh()
}

func (b *TeleBot) handleResume(ctx context.Context, chatID int64) {
	if err := b.adminClient.Resume(ctx); err != nil {
		b.reply(chatID, formatAdminError("Resume", err))
		return
	}
	b.reply(chatID, "▶️ Bundle submission *resumed*")
	b.triggerRefresh()
}

func (b *TeleBot) handleReset(ctx context.Context, chatID int64) {
	b.mu.Lock()
	b.resetPending[chatID] = true
	b.mu.Unlock()
	b.reply(chatID, "⚠️ System is *Halted*. Type /reset_confirm to reset daily counters and resume. Use only after investigating the halt reason.")
}

func (b *TeleBot) handleResetConfirm(ctx context.Context, chatID int64) {
	b.mu.Lock()
	pending := b.resetPending[chatID]
	delete(b.resetPending, chatID)
	b.mu.Unlock()
	if !pending {
		b.reply(chatID, "No pending reset. Send /reset first.")
		return
	}
	if err := b.adminClient.Reset(ctx); err != nil {
		b.reply(chatID, formatAdminError("Reset", err))
		return
	}
	b.reply(chatID, "✅ System reset from *Halted* — now *Running*")
	b.triggerRefresh()
}

func formatAdminError(action string, err error) string {
	msg := err.Error()
	if strings.Contains(msg, "409") || strings.Contains(msg, "Conflict") {
		if strings.Contains(msg, "Halted") {
			return fmt.Sprintf("❌ Cannot %s: system is halted. Use /reset if appropriate.", strings.ToLower(action))
		}
		return fmt.Sprintf("❌ Cannot %s: %s", strings.ToLower(action), friendlyConflict(msg))
	}
	return fmt.Sprintf("❌ %s failed: %v", action, err)
}

func friendlyConflict(msg string) string {
	if strings.Contains(msg, "already") || strings.Contains(msg, "invalid transition") {
		return "state transition not allowed in current state"
	}
	return msg
}

func (b *TeleBot) handleSetMinProfit(ctx context.Context, chatID int64, args string) {
	val, err := strconv.ParseFloat(strings.TrimSpace(args), 64)
	if err != nil || val <= 0 {
		b.reply(chatID, "Usage: /set_min_profit 0.001")
		return
	}
	if err := b.adminClient.SetMinProfit(ctx, val); err != nil {
		b.reply(chatID, fmt.Sprintf("❌ Failed: %v", err))
		return
	}
	b.reply(chatID, fmt.Sprintf("✅ Min profit set to `%.6f ETH`", val))
}

func (b *TeleBot) fetchSnapshot(ctx context.Context) (metrics.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return b.metricsClient.FetchSnapshot(ctx)
}

func (b *TeleBot) reply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown"
	if _, err := b.api.Send(msg); err != nil {
		slog.Error("send message failed", "err", err)
	}
}

func dashboardKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🔄 Refresh", "refresh"),
			tgbotapi.NewInlineKeyboardButtonData("🏊 Pools", "pools"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("🏥 Health", "health"),
			tgbotapi.NewInlineKeyboardButtonData("📈 Trades", "trades"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⏸ Pause", "pause"),
			tgbotapi.NewInlineKeyboardButtonData("▶️ Resume", "resume"),
		),
	)
}
