package bot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/user"
)

const (
	// workers — сколько сообщений обрабатывается параллельно. Ответ LLM занимает
	// секунды, и с одним воркером члены семьи ждали бы друг друга.
	workers = 4
	// handleTimeout — сколько можно обрабатывать одно сообщение, включая ответ LLM
	// с повторами. Столько же бот ждёт сообщения в работе при остановке.
	handleTimeout = 2 * time.Minute
	// typingEvery — Telegram гасит «печатает...» через 5 секунд, а ответ модели
	// идёт дольше, поэтому индикатор обновляется.
	typingEvery = 4 * time.Second
)

const (
	greeting = "Привет! Я записываю семейные траты. Просто напиши, например: «кофе 350» или «такси 1200 вчера»."
	notText  = "Я понимаю только текстовые сообщения."
	failed   = "Что-то пошло не так, попробуй ещё раз."
)

// Agent понимает сообщение пользователя и возвращает ответ.
// Бот ничего не знает про LLM: так агента можно проверять и без Telegram.
type Agent interface {
	Reply(ctx context.Context, userID int64, text string) (string, error)
}

// UserSaver регистрирует членов семьи: кто написал боту, тот и член семьи.
type UserSaver interface {
	Upsert(ctx context.Context, u user.User) error
}

type Bot struct {
	api   *tgbot.Bot
	agent Agent
	users UserSaver
	log   *slog.Logger
}

// New подключается к Telegram и проверяет токен (запрос getMe).
func New(token string, users UserSaver, agent Agent, log *slog.Logger) (*Bot, error) {
	b := &Bot{agent: agent, users: users, log: log}

	api, err := tgbot.New(token,
		tgbot.WithDefaultHandler(b.handle),
		// По умолчанию библиотека запускает отдельную горутину на каждое сообщение,
		// и WithWorkers ничего не ограничивает. Синхронные обработчики: не больше
		// workers сообщений одновременно, и Run ждёт их завершения при остановке.
		tgbot.WithNotAsyncHandlers(),
		tgbot.WithWorkers(workers),
		tgbot.WithErrorsHandler(func(err error) {
			log.Error("telegram", "error", err)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to telegram: %w", err)
	}
	b.api = api
	return b, nil
}

// Run опрашивает Telegram (long polling) и блокируется, пока не отменят ctx
// и не закончится обработка сообщений, которые уже в работе.
func (b *Bot) Run(ctx context.Context) {
	b.api.Start(ctx)
}

func (b *Bot) handle(runCtx context.Context, _ *tgbot.Bot, upd *models.Update) {
	// Остановка бота не обрывает начатое сообщение: трата не запишется
	// без ответа пользователю. Но и бесконечно ждать не будем.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(runCtx), handleTimeout)
	defer cancel()

	msg := upd.Message
	// Нас интересуют только личные сообщения от людей: правки, реакции,
	// сообщения в группах и от других ботов пропускаем.
	if msg == nil || msg.From == nil || msg.From.IsBot || msg.Chat.Type != models.ChatTypePrivate {
		return
	}

	from := msg.From
	log := b.log.With("user_id", from.ID)
	log.Debug("message received", "text_len", utf8.RuneCountInString(msg.Text))

	if msg.Text == "" {
		b.send(ctx, msg.Chat.ID, notText)
		return
	}

	// Регистрируем при каждом сообщении: заодно обновляется имя, если его сменили в Telegram.
	if err := b.users.Upsert(ctx, toUser(from)); err != nil {
		log.Error("save user", "error", err)
		b.send(ctx, msg.Chat.ID, failed)
		return
	}

	if isStart(msg.Text) {
		b.send(ctx, msg.Chat.ID, greeting)
		return
	}

	stopTyping := b.keepTyping(ctx, msg.Chat.ID)
	reply, err := b.agent.Reply(ctx, from.ID, msg.Text)
	stopTyping()
	if err != nil {
		log.Error("agent reply", "error", err)
		b.send(ctx, msg.Chat.ID, failed)
		return
	}
	b.send(ctx, msg.Chat.ID, reply)
}

// isStart узнаёт команду /start, в том числе с упоминанием бота: /start@my_bot.
func isStart(text string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	cmd, _, _ = strings.Cut(cmd, "@")
	return cmd == "/start"
}

// keepTyping показывает «печатает...», пока агент думает. Возвращает функцию остановки.
func (b *Bot) keepTyping(ctx context.Context, chatID int64) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(typingEvery)
		defer ticker.Stop()
		for {
			_, _ = b.api.SendChatAction(ctx, &tgbot.SendChatActionParams{
				ChatID: chatID,
				Action: models.ChatActionTyping,
			})
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		cancel()
		<-done // горутина не переживет обработку сообщения
	}
}

func (b *Bot) send(ctx context.Context, chatID int64, text string) {
	if _, err := b.api.SendMessage(ctx, &tgbot.SendMessageParams{ChatID: chatID, Text: text}); err != nil {
		b.log.Error("send message", "chat_id", chatID, "error", err)
	}
}

func toUser(from *models.User) user.User {
	u := user.User{ID: from.ID, FirstName: from.FirstName}
	if from.Username != "" {
		u.Username = &from.Username
	}
	return u
}
