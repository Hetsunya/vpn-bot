// Package bot contains Telegram delivery handlers.
package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/telebot.v3"

	"vpn-bot/internal/client/panel"
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

	// Base tariff from .env.
	defaultPrice        string
	defaultDurationDays int

	mu               sync.Mutex
	waitingForServer map[int64]bool
}

type tariff struct {
	Months   int
	Days     int
	Discount int
}

func NewBot(
	token string,
	subService *service.SubscriptionService,
	payService *service.PaymentService,
	userRepo repository.UserRepo,
	serverRepo repository.ServerRepo,
	adminIDs []int64,
	defaultPrice string,
	defaultDurationDays int,
) (*Bot, error) {
	pref := telebot.Settings{
		Token:  token,
		Poller: &telebot.LongPoller{Timeout: 10 * time.Second},
	}

	tb, err := telebot.NewBot(pref)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &Bot{
		tb:                  tb,
		subService:          subService,
		payService:          payService,
		userRepo:            userRepo,
		serverRepo:          serverRepo,
		adminIDs:            adminIDs,
		defaultPrice:        defaultPrice,
		defaultDurationDays: defaultDurationDays,
		waitingForServer:    make(map[int64]bool),
	}, nil
}

func (b *Bot) Start() {
	b.registerHandlers()
	b.tb.Start()
}

func (b *Bot) Stop() {
	b.tb.Stop()
}

func (b *Bot) registerHandlers() {
	b.tb.Use(b.registerUser)

	// Пользовательские команды.
	b.tb.Handle("/start", b.handleStart)
	b.tb.Handle("/cancel", b.handleCancel)

	// Главное меню.
	b.tb.Handle(&telebot.InlineButton{Unique: "buy"}, b.handleBuyMenu)
	b.tb.Handle(&telebot.InlineButton{Unique: "my_sub"}, b.handleMySub)
	b.tb.Handle(&telebot.InlineButton{Unique: "about"}, b.handleAbout)
	b.tb.Handle(&telebot.InlineButton{Unique: "back_main"}, b.handleBackMain)

	// Тарифы.
	b.tb.Handle(&telebot.InlineButton{Unique: "tariff"}, b.handleTariff)

	// Способы оплаты.
	b.tb.Handle(&telebot.InlineButton{Unique: "pay_crypto"}, b.handleCryptoPayment)

	// Моя подписка.
	b.tb.Handle(&telebot.InlineButton{Unique: "refresh_sub"}, b.handleMySub)

	// Администрирование.
	b.tb.Handle("/admin", b.handleAdmin)
	b.tb.Handle(&telebot.InlineButton{Unique: "add_server"}, b.handleAddServer)
	b.tb.Handle(&telebot.InlineButton{Unique: "stats"}, b.handleStats)

	// Используется только во время добавления сервера администратором.
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
			newUser := &model.User{
				TgID:      user.ID,
				Username:  user.Username,
				CreatedAt: time.Now().UTC(),
			}

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

// =========================
// Главное меню
// =========================

func (b *Bot) mainMenu() *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.Data("🛒 Купить подписку", "buy"),
		),
		markup.Row(
			markup.Data("🔑 Моя подписка", "my_sub"),
			markup.Data("ℹ️ О сервисе", "about"),
		),
	)

	return markup
}

func (b *Bot) handleStart(c telebot.Context) error {
	text := `👋 <b>Добро пожаловать!</b>

Сервис предоставляет VPN-доступ для безопасного и конфиденциального использования интернета.

Выберите нужный раздел:`

	return c.Send(
		text,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		b.mainMenu(),
	)
}

func (b *Bot) handleBackMain(c telebot.Context) error {
	err := c.Edit(
		`👋 <b>Главное меню</b>

Выберите нужный раздел:`,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		b.mainMenu(),
	)

	if err != nil {
		return err
	}

	return c.Respond()
}

// =========================
// Покупка подписки
// =========================

func (b *Bot) handleBuyMenu(c telebot.Context) error {
	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.Data(
				fmt.Sprintf("1 месяц — %s USDT", b.tariffPrice(1)),
				"tariff",
				"1",
			),
		),
		markup.Row(
			markup.Data(
				fmt.Sprintf("3 месяца — %s USDT (-10%%)", b.tariffPrice(3)),
				"tariff",
				"3",
			),
		),
		markup.Row(
			markup.Data(
				fmt.Sprintf("6 месяцев — %s USDT (-15%%)", b.tariffPrice(6)),
				"tariff",
				"6",
			),
		),
		markup.Row(
			markup.Data(
				fmt.Sprintf("12 месяцев — %s USDT (-20%%)", b.tariffPrice(12)),
				"tariff",
				"12",
			),
		),
		markup.Row(
			markup.Data("◀️ Назад", "back_main"),
		),
	)

	err := c.Edit(
		`🛒 <b>Покупка подписки</b>

Выберите срок подписки:

Чем больше срок подписки, тем ниже стоимость одного месяца.`,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)

	if err != nil {
		return err
	}

	return c.Respond()
}

func (b *Bot) handleTariff(c telebot.Context) error {
	months, err := strconv.Atoi(c.Data())
	if err != nil {
		return c.Respond(&telebot.CallbackResponse{
			Text:      "Не удалось определить тариф.",
			ShowAlert: true,
		})
	}

	t, ok := b.getTariff(months)
	if !ok {
		return c.Respond(&telebot.CallbackResponse{
			Text:      "Такого тарифа нет.",
			ShowAlert: true,
		})
	}

	price := b.tariffPrice(months)

	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.Data(
				"💎 Оплатить криптовалютой",
				"pay_crypto",
				strconv.Itoa(months),
			),
		),
		markup.Row(
			markup.Data("◀️ Выбрать другой срок", "buy"),
		),
		markup.Row(
			markup.Data("🏠 Главное меню", "back_main"),
		),
	)

	text := fmt.Sprintf(
		`📦 <b>Тариф: %d %s</b>

💵 К оплате: <b>%s USDT</b>
📅 Срок: <b>%d дней</b>

Выберите способ оплаты:`,
		t.Months,
		monthsWord(t.Months),
		price,
		t.Days,
	)

	err = c.Edit(
		text,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)
	if err != nil {
		return err
	}

	return c.Respond()
}

func (b *Bot) handleCryptoPayment(c telebot.Context) error {
	months, err := strconv.Atoi(c.Data())
	if err != nil {
		return c.Respond(&telebot.CallbackResponse{
			Text:      "Не удалось определить тариф.",
			ShowAlert: true,
		})
	}

	t, ok := b.getTariff(months)
	if !ok {
		return c.Respond(&telebot.CallbackResponse{
			Text:      "Такого тарифа нет.",
			ShowAlert: true,
		})
	}

	price := b.tariffPrice(months)

	ctx := botContext(c)

	payURL, err := b.payService.CreateInvoice(
		ctx,
		c.Sender().ID,
		price,
		t.Days,
	)
	if err != nil {
		log.Printf(
			"bot: create crypto invoice for %d: %v",
			c.Sender().ID,
			err,
		)

		return c.Respond(&telebot.CallbackResponse{
			Text:      "Не удалось создать счёт. Попробуйте позже.",
			ShowAlert: true,
		})
	}

	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.URL("💳 Оплатить через CryptoBot", payURL),
		),
		markup.Row(
			markup.URL(
				"📄 Политика конфиденциальности",
				"https://telegra.ph/Politika-konfidencialnosti-11-23-30",
			),
		),
		markup.Row(
			markup.URL(
				"📄 Пользовательское соглашение",
				"https://telegra.ph/Polzovatelskoe-soglashenie-02-11-46",
			),
		),
		markup.Row(
			markup.Data("◀️ Назад", "buy"),
		),
	)

	text := fmt.Sprintf(
		`💎 <b>Оплата через криптовалюту</b>

📦 Тариф: <b>%d %s</b>
💵 К оплате: <b>%s USDT</b>

⚡️ Оплата происходит через @CryptoBot.
💰 Оплата принимается в USDT.

После успешной оплаты подписка активируется автоматически.

📄 Нажимая кнопку оплаты, вы соглашаетесь с Политикой конфиденциальности и Пользовательским соглашением.`,
		t.Months,
		monthsWord(t.Months),
		price,
	)

	err = c.Edit(
		text,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)
	if err != nil {
		return err
	}

	return c.Respond()
}

// =========================
// Моя подписка
// =========================

func (b *Bot) handleMySub(c telebot.Context) error {
	sub, traffic, err := b.subService.GetActiveSubscription(
		botContext(c),
		c.Sender().ID,
	)

	if err != nil || sub == nil {
		if err != nil && !errors.Is(err, service.ErrNoActiveSubscription) {
			log.Printf(
				"bot: get subscription for %d: %v",
				c.Sender().ID,
				err,
			)
		}

		if c.Callback() != nil {
			return c.Edit(
				`🔑 <b>Моя подписка</b>

У вас нет активной подписки.

Оформите подписку, чтобы получить доступ к VPN.`,
				&telebot.SendOptions{
					ParseMode: telebot.ModeHTML,
				},
				b.mySubscriptionMenu(),
			)
		}

		return c.Send(
			`🔑 <b>Моя подписка</b>

У вас нет активной подписки.

Оформите подписку, чтобы получить доступ к VPN.`,
			&telebot.SendOptions{
				ParseMode: telebot.ModeHTML,
			},
			b.mySubscriptionMenu(),
		)
	}

	message := fmt.Sprintf(
		`🔑 <b>Моя подписка</b>

🟢 Статус: <b>активна</b>

📅 Действует до:
<b>%s</b>

📊 Трафик:
<b>%s</b>

🔗 <b>Ссылка на подписку:</b>

<code>%s</code>

💡 Скопируйте ссылку и добавьте её в ваше VPN-приложение:

• Hiddify / HiddifyNG
• v2rayNG (Android)
• Streisand (iOS)
• NekoBox`,
		sub.ExpiresAt.Format("02.01.2006 15:04"),
		formatTraffic(traffic),
		sub.SubscriptionURL,
	)

	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.Data("🔄 Обновить", "refresh_sub"),
		),
		markup.Row(
			markup.Data("🛒 Продлить подписку", "buy"),
		),
		markup.Row(
			markup.Data("🏠 Главное меню", "back_main"),
		),
	)

	if c.Callback() != nil {
		err := c.Edit(
			message,
			&telebot.SendOptions{
				ParseMode: telebot.ModeHTML,
			},
			markup,
		)
		if err != nil {
			return err
		}

		return c.Respond()
	}

	return c.Send(
		message,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)
}

func (b *Bot) mySubscriptionMenu() *telebot.ReplyMarkup {
	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.Data("🛒 Купить подписку", "buy"),
		),
		markup.Row(
			markup.Data("🏠 Главное меню", "back_main"),
		),
	)

	return markup
}

// =========================
// О сервисе
// =========================

func (b *Bot) handleAbout(c telebot.Context) error {
	markup := &telebot.ReplyMarkup{}

	markup.Inline(
		markup.Row(
			markup.URL(
				"📄 Политика конфиденциальности",
				"https://telegra.ph/Politika-konfidencialnosti-09-11-54",
			),
		),
		markup.Row(
			markup.URL(
				"📄 Пользовательское соглашение",
				"https://telegra.ph/Polzovatelskoe-soglashenie-09-11-25",
			),
		),
		markup.Row(
			markup.Data("🏠 Главное меню", "back_main"),
		),
	)

	text := `ℹ️ <b>О сервисе</b>

Наш сервис предоставляет VPN-доступ для защиты соединения и повышения приватности при использовании интернета.

<b>🔰 Наши принципы</b>

✅ Безопасность соединения
✅ Конфиденциальность пользователей
✅ Автоматическая активация подписки
✅ Стабильная работа инфраструктуры
✅ Прозрачные условия использования

<b>⚠️ Важно</b>

Мы не гарантируем доступность отдельных сайтов и сервисов, поскольку на их работу могут влиять действия интернет-провайдеров, государственных органов и третьих лиц.

Сервис предназначен для законного использования. Пользователь самостоятельно несёт ответственность за соблюдение законодательства своей страны.`

	err := c.Edit(
		text,
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)
	if err != nil {
		return err
	}

	return c.Respond()
}

// =========================
// Тарифы
// =========================

func (b *Bot) getTariff(months int) (tariff, bool) {
	switch months {
	case 1:
		return tariff{
			Months:   1,
			Days:     b.defaultDurationDays,
			Discount: 0,
		}, true

	case 3:
		return tariff{
			Months:   3,
			Days:     b.defaultDurationDays * 3,
			Discount: 10,
		}, true

	case 6:
		return tariff{
			Months:   6,
			Days:     b.defaultDurationDays * 6,
			Discount: 15,
		}, true

	case 12:
		return tariff{
			Months:   12,
			Days:     b.defaultDurationDays * 12,
			Discount: 20,
		}, true

	default:
		return tariff{}, false
	}
}

func (b *Bot) tariffPrice(months int) string {
	t, ok := b.getTariff(months)
	if !ok {
		return b.defaultPrice
	}

	basePrice, err := strconv.ParseFloat(b.defaultPrice, 64)
	if err != nil {
		return b.defaultPrice
	}

	total := basePrice * float64(t.Months)

	if t.Discount > 0 {
		total *= 1 - float64(t.Discount)/100
	}

	return fmt.Sprintf("%.2f", total)
}

func monthsWord(months int) string {
	switch {
	case months%10 == 1 && months%100 != 11:
		return "месяц"

	case months%10 >= 2 &&
		months%10 <= 4 &&
		(months%100 < 10 || months%100 >= 20):
		return "месяца"

	default:
		return "месяцев"
	}
}

// =========================
// Traffic
// =========================

func formatTraffic(t *panel.ClientTraffic) string {
	if t == nil {
		return "0 MB / ∞"
	}

	return formatBytes(t.Up+t.Down) + " / " + func() string {
		if t.Total <= 0 {
			return "∞"
		}

		return formatBytes(t.Total)
	}()
}

func formatBytes(v int64) string {
	const gb = int64(1024 * 1024 * 1024)
	const mb = int64(1024 * 1024)

	if v < gb {
		return fmt.Sprintf("%.0f MB", float64(v)/float64(mb))
	}

	return fmt.Sprintf("%.2f GB", float64(v)/float64(gb))
}

// =========================
// Admin
// =========================

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

	markup.Inline(
		markup.Row(
			markup.Data("➕ Добавить сервер", "add_server"),
		),
		markup.Row(
			markup.Data("📊 Статистика", "stats"),
		),
	)

	return c.Send(
		"⚙️ <b>Панель администратора</b>",
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
		markup,
	)
}

func (b *Bot) handleAddServer(c telebot.Context) error {
	if !b.isAdmin(c.Sender().ID) {
		return c.Respond()
	}

	b.mu.Lock()
	b.waitingForServer[c.Sender().ID] = true
	b.mu.Unlock()

	if err := c.Send(
		`Отправьте данные сервера в формате:

Name: Server 1
URL: http://...
Username: ...
Password: ...

Или /cancel для отмены.`,
	); err != nil {
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
	if !b.isAdmin(c.Sender().ID) ||
		!b.isWaitingForServer(c.Sender().ID) {
		return nil
	}

	name, panelURL, password, err := parseServerInput(c.Text())
	if err != nil {
		return c.Send(
			"Неверный формат. Укажите URL и Password. Используйте /cancel для отмены.",
		)
	}

	server := &model.Server{
		Name:      name,
		PanelURL:  panelURL,
		APISecret: password,
		IsActive:  true,
	}

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
		log.Printf(
			"bot: get statistics: users=%v subscriptions=%v",
			userErr,
			subErr,
		)

		return c.Send("Не удалось получить статистику.")
	}

	err := c.Send(
		fmt.Sprintf(
			"📊 <b>Статистика</b>\n\nПользователей: %d\nАктивных подписок: %d",
			users,
			active,
		),
		&telebot.SendOptions{
			ParseMode: telebot.ModeHTML,
		},
	)

	if err != nil {
		return err
	}

	return c.Respond()
}

func (b *Bot) isWaitingForServer(tgID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.waitingForServer[tgID]
}

// =========================
// Helpers
// =========================

func botContext(c telebot.Context) context.Context {
	if ctx, ok := c.Get("context").(context.Context); ok && ctx != nil {
		return ctx
	}

	return context.Background()
}

func parseServerInput(text string) (
	name,
	panelURL,
	password string,
	err error,
) {
	values := make(map[string]string)

	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok {
			values[strings.ToLower(strings.TrimSpace(key))] =
				strings.TrimSpace(value)
		}
	}

	panelURL = values["url"]
	password = values["password"]

	if panelURL == "" || password == "" {
		return "", "", "", fmt.Errorf(
			"URL and Password are required",
		)
	}

	name = values["name"]

	if name == "" {
		name = "Server " + time.Now().Format("20060102150405")
	}

	return name, panelURL, password, nil
}
