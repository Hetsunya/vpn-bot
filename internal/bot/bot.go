// Package bot contains Telegram delivery handlers.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v3"

	"vpn-bot/internal/model"
	"vpn-bot/internal/repository"
	"vpn-bot/internal/service"
)

type Bot struct {
	tb         *telebot.Bot
	subService *service.SubscriptionService
	payService *service.PaymentService
	userRepo   repository.UserRepo
	serverRepo repository.ServerRepo
	adminIDs   []int64

	mu               sync.Mutex
	waitingForServer map[int64]bool
}

func NewBot(token string, subService *service.SubscriptionService, payService *service.PaymentService, userRepo repository.UserRepo, serverRepo repository.ServerRepo, adminIDs []int64) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}
	tb, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}
	return &Bot{tb: tb, subService: subService, payService: payService, userRepo: userRepo, serverRepo: serverRepo, adminIDs: adminIDs, waitingForServer: make(map[int64]bool)}, nil
}

func (b *Bot) Start() {
	b.registerHandlers()
	b.tb.Start()
}

func (b *Bot) Stop() { b.tb.Stop() }

func (b *Bot) registerHandlers() {
	b.tb.Use(b.registerUser)
	b.tb.Handle("/start", b.handleStart)
	b.tb.Handle("/buy", b.handleBuy)
	b.tb.Handle("/my_sub", b.handleMySub)
	b.tb.Handle("/admin", b.handleAdmin)
	b.tb.Handle("/cancel", b.handleCancel)
	b.tb.Handle(&telebot.InlineButton{Unique: "add_server"}, b.handleAddServer)
	b.tb.Handle(&telebot.InlineButton{Unique: "stats"}, b.handleStats)
	b.tb.Handle(telebot.OnText, b.handleServerInput)
}

func (b *Bot) registerUser(next telebot.HandlerFunc) telebot.HandlerFunc {
	return func(c telebot.Context) error {
		ctx, _ := c.Get("context").(context.Context)
		if ctx == nil {
			ctx = context.Background()
			c.Set("context", ctx)
		}
		user := c.Sender()
		if user == nil {
			return next(c)
		}
		_, err := b.userRepo.GetByTgID(ctx, user.ID)
		if errors.Is(err, repository.ErrNotFound) {
			newUser := &model.User{TgID: user.ID, Username: user.Username, CreatedAt: time.Now().UTC()}
			if err := b.userRepo.Create(ctx, newUser); err != nil {
				log.Printf("bot: register user %d: %v", user.ID, err)
				return fmt.Errorf("register user: %w", err)
			}
		} else if err != nil {
			log.Printf("bot: find user %d: %v", user.ID, err)
			return fmt.Errorf("get user: %w", err)
		}
		return next(c)
	}
}

func (b *Bot) handleStart(c telebot.Context) error {
	return c.Send("Добро пожаловать! Используйте /buy для покупки подписки или /my_sub для просмотра вашей подписки.")
}

func (b *Bot) handleBuy(c telebot.Context) error {
	ctx := botContext(c)
	payURL, err := b.payService.CreateInvoice(ctx, c.Sender().ID, "1.00", 30)
	if err != nil {
		log.Printf("bot: create invoice for %d: %v", c.Sender().ID, err)
		return c.Send("Ошибка создания платежа. Попробуйте позже.")
	}
	markup := &telebot.ReplyMarkup{}
	markup.Inline(markup.Row(markup.URL("💳 Оплатить", payURL)))
	return c.Send("Нажмите кнопку ниже для оплаты:", markup)
}

func (b *Bot) handleMySub(c telebot.Context) error {
	sub, vlessLink, err := b.subService.GetActiveSubscription(botContext(c), c.Sender().ID)
	if err != nil || sub == nil {
		if err != nil && !errors.Is(err, service.ErrNoActiveSubscription) {
			log.Printf("bot: get subscription for %d: %v", c.Sender().ID, err)
		}
		return c.Send("У вас нет активной подписки. Используйте /buy для покупки.")
	}
	message := fmt.Sprintf("🔑 Ваша подписка активна до: %s\n\nСкопируйте ссылку ниже в ваше VPN-приложение:\n\n`%s`", sub.ExpiresAt.Format("02.01.2006 15:04"), vlessLink)
	return c.Send(message, telebot.ModeMarkdown)
}

func (b *Bot) isAdmin(tgID int64) bool {
	for _, id := range b.adminIDs {
		if id == tgID {
			return true
		}
	}
	return false
}

func (b *Bot) handleAdmin(c telebot.Context) error {
	if !b.isAdmin(c.Sender().ID) {
		return c.Send("Доступ запрещен.")
	}
	markup := &telebot.ReplyMarkup{}
	markup.Inline(markup.Row(markup.Data("➕ Добавить сервер", "add_server")), markup.Row(markup.Data("📊 Статистика", "stats")))
	return c.Send("Панель администратора:", markup)
}

func (b *Bot) handleAddServer(c telebot.Context) error {
	if !b.isAdmin(c.Sender().ID) {
		return c.Respond()
	}
	b.mu.Lock()
	b.waitingForServer[c.Sender().ID] = true
	b.mu.Unlock()
	if err := c.Send("Отправьте данные сервера в формате:\n\nName: Server 1\nURL: http://...\nUsername: ...\nPassword: ...\n\nИли /cancel для отмены."); err != nil {
		return err
	}
	return c.Respond()
}

func (b *Bot) handleCancel(c telebot.Context) error {
	b.mu.Lock()
	delete(b.waitingForServer, c.Sender().ID)
	b.mu.Unlock()
	return c.Send("Действие отменено.")
}

func (b *Bot) handleServerInput(c telebot.Context) error {
	if !b.isAdmin(c.Sender().ID) || !b.isWaitingForServer(c.Sender().ID) {
		return nil
	}
	name, panelURL, password, err := parseServerInput(c.Text())
	if err != nil {
		return c.Send("Неверный формат. Укажите URL и Password. Используйте /cancel для отмены.")
	}
	server := &model.Server{Name: name, PanelURL: panelURL, APISecret: password, IsActive: true}
	if err := b.serverRepo.Create(botContext(c), server); err != nil {
		log.Printf("bot: add server: %v", err)
		return c.Send("Ошибка добавления сервера.")
	}
	b.mu.Lock()
	delete(b.waitingForServer, c.Sender().ID)
	b.mu.Unlock()
	return c.Send("✅ Сервер успешно добавлен!")
}

func (b *Bot) handleStats(c telebot.Context) error {
	if !b.isAdmin(c.Sender().ID) {
		return c.Respond()
	}
	ctx := botContext(c)
	users, userErr := b.userRepo.GetTotal(ctx)
	active, subErr := b.subService.GetActiveCount(ctx)
	if userErr != nil || subErr != nil {
		log.Printf("bot: get statistics: users=%v subscriptions=%v", userErr, subErr)
		return c.Send("Не удалось получить статистику.")
	}
	if err := c.Send(fmt.Sprintf("📊 Статистика:\nПользователей: %d\nАктивных подписок: %d", users, active)); err != nil {
		return err
	}
	return c.Respond()
}

func (b *Bot) isWaitingForServer(tgID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.waitingForServer[tgID]
}

func botContext(c telebot.Context) context.Context {
	if ctx, ok := c.Get("context").(context.Context); ok && ctx != nil {
		return ctx
	}
	return context.Background()
}

func parseServerInput(text string) (name, panelURL, password string, err error) {
	values := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	panelURL, password = values["url"], values["password"]
	if panelURL == "" || password == "" {
		return "", "", "", fmt.Errorf("URL and Password are required")
	}
	name = values["name"]
	if name == "" {
		name = "Server " + time.Now().Format("20060102150405")
	}
	return name, panelURL, password, nil
}
