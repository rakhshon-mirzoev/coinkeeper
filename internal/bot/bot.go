package bot

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/rakhshon-mirzoev/coinkeeper/internal/user"
)

// workers — сколько сообщений обрабатывается параллельно. Ответ LLM занимает
// секунды, и с одним воркером члены семьи ждали бы друг друга.
const workers = 4

const (
	greeting = "Привет! Я записываю семейные траты. Просто напиши, например: «кофе 350» или «такси 1200 вчера»."
	notText  = "Я понимаю только текстовые сообщения."
	denied   = "Это семейный бот, у тебя нет к нему доступа."
	failed   = "Что-то пошло не так, попробуй ещё раз."
)

// Agent понимает сообщение пользователя и возвращает ответ.
// Бот ничего не знает про LLM: так агента можно проверять и без Telegram.
type Agent interface {
	Reply(ctx context.Context, userID int64, text string) (string, error)
}

type UserSaver interface {
	Upsert(ctx context.Context, u user.User) error
}

type Bot struct {
	api     *tgbot.Bot
	agent   Agent
	users   UserSaver
	allowed map[int64]bool
	log     *slog.Logger
}

// New подключается к Telegram и проверяет токен (запрос getMe).
// allowedIDs пустой — бот отвечает всем.
func New(token string, allowedIDs []int64, users UserSaver, agent Agent, log *slog.Logger) (*Bot, error) {
	b := &Bot{
		agent:   agent,
		users:   users,
		allowed: make(map[int64]bool, len(allowedIDs)),
		log:     log,
	}
	for _, id := range allowedIDs {
		b.allowed[id] = true
	}

	api, err := tgbot.New(token,
		tgbot.WithDefaultHandler(b.handle),
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

// Run опрашивает Telegram (long polling) и блокируется, пока не отменят ctx.
func (b *Bot) Run(ctx context.Context) {
	b.api.Start(ctx)
}

func (b *Bot) handle(ctx context.Context, _ *tgbot.Bot, upd *models.Update) {
	msg := upd.Message
	// Нас интересуют только личные сообщения от людей: правки, реакции,
	// сообщения в группах и от других ботов пропускаем.
	if msg == nil || msg.From == nil || msg.From.IsBot || msg.Chat.Type != models.ChatTypePrivate {
		return
	}

	from := msg.From
	log := b.log.With("user_id", from.ID)
	log.Debug("message received", "text_len", utf8.RuneCountInString(msg.Text))

	if !b.isAllowed(from.ID) {
		log.Warn("access denied")
		b.send(ctx, msg.Chat.ID, denied)
		return
	}
	if msg.Text == "" {
		b.send(ctx, msg.Chat.ID, notText)
		return
	}

	if err := b.users.Upsert(ctx, toUser(from)); err != nil {
		log.Error("save user", "error", err)
		b.send(ctx, msg.Chat.ID, failed)
		return
	}

	if msg.Text == "/start" {
		b.send(ctx, msg.Chat.ID, greeting)
		return
	}

	// «печатает...» в чате, пока агент думает.
	_, _ = b.api.SendChatAction(ctx, &tgbot.SendChatActionParams{
		ChatID: msg.Chat.ID,
		Action: models.ChatActionTyping,
	})

	reply, err := b.agent.Reply(ctx, from.ID, msg.Text)
	if err != nil {
		log.Error("agent reply", "error", err)
		b.send(ctx, msg.Chat.ID, failed)
		return
	}
	b.send(ctx, msg.Chat.ID, reply)
}

func (b *Bot) isAllowed(userID int64) bool {
	return len(b.allowed) == 0 || b.allowed[userID]
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
