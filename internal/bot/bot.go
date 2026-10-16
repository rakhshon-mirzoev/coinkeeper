package bot

import (
	"context"
	"crypto/subtle"
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
	greeting  = "Привет! Я записываю семейные траты. Просто напиши, например: «кофе 350» или «такси 1200 вчера»."
	welcome   = "Добро пожаловать в семью! " + greeting
	notText   = "Я понимаю только текстовые сообщения."
	needCode  = "Это семейный бот. Чтобы пользоваться им, попроси у главы семьи ссылку-приглашение."
	wrongCode = "Неверная ссылка-приглашение. Попроси у главы семьи новую."
	failed    = "Что-то пошло не так, попробуй ещё раз."
)

// Agent понимает сообщение пользователя и возвращает ответ.
// Бот ничего не знает про LLM: так агента можно проверять и без Telegram.
type Agent interface {
	Reply(ctx context.Context, userID int64, text string) (string, error)
}

// UserStore — члены семьи. Кто есть в хранилище, тот член семьи.
type UserStore interface {
	Upsert(ctx context.Context, u user.User) error
	Exists(ctx context.Context, id int64) (bool, error)
}

type Bot struct {
	api   *tgbot.Bot
	agent Agent
	users UserStore
	// inviteCode — код ссылки t.me/<бот>?start=<код>. Пустой — бот открыт для всех,
	// иначе пользоваться им могут только пришедшие по ссылке.
	inviteCode string
	log        *slog.Logger
}

// New подключается к Telegram и проверяет токен (запрос getMe).
func New(token, inviteCode string, users UserStore, agent Agent, log *slog.Logger) (*Bot, error) {
	b := newBot(inviteCode, users, agent, log)

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

func newBot(inviteCode string, users UserStore, agent Agent, log *slog.Logger) *Bot {
	return &Bot{agent: agent, users: users, inviteCode: inviteCode, log: log}
}

// InviteLink — ссылка-приглашение для семейного чата. Пустая, если код не задан.
func (b *Bot) InviteLink(ctx context.Context) (string, error) {
	if b.inviteCode == "" {
		return "", nil
	}
	me, err := b.api.GetMe(ctx)
	if err != nil {
		return "", fmt.Errorf("get bot info: %w", err)
	}
	return fmt.Sprintf("https://t.me/%s?start=%s", me.Username, b.inviteCode), nil
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

	verdict, err := b.checkAccess(ctx, from.ID, msg.Text)
	if err != nil {
		log.Error("check access", "error", err)
		b.send(ctx, msg.Chat.ID, failed)
		return
	}
	switch verdict {
	case accessDenied:
		log.Warn("access denied")
		b.send(ctx, msg.Chat.ID, needCode)
		return
	case accessWrongCode:
		log.Warn("wrong invite code")
		b.send(ctx, msg.Chat.ID, wrongCode)
		return
	}

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

	if verdict == accessJoined {
		log.Info("joined family by invite")
		b.send(ctx, msg.Chat.ID, welcome)
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

type accessVerdict int

const (
	accessGranted   accessVerdict = iota // уже член семьи или бот открыт
	accessJoined                         // пришёл по верной ссылке-приглашению
	accessWrongCode                      // пришёл по ссылке с неверным кодом
	accessDenied
)

// checkAccess решает, можно ли пользователю работать с ботом.
func (b *Bot) checkAccess(ctx context.Context, userID int64, text string) (accessVerdict, error) {
	if b.inviteCode == "" {
		return accessGranted, nil // приглашения выключены — бот открыт
	}

	member, err := b.users.Exists(ctx, userID)
	if err != nil {
		return 0, err
	}
	if member {
		return accessGranted, nil
	}

	code, ok := startPayload(text)
	if !ok || code == "" {
		return accessDenied, nil
	}

	if subtle.ConstantTimeCompare([]byte(code), []byte(b.inviteCode)) == 1 {
		return accessJoined, nil
	}
	return accessWrongCode, nil
}

func startPayload(text string) (string, bool) {
	if !isStart(text) {
		return "", false
	}
	_, payload, _ := strings.Cut(strings.TrimSpace(text), " ")
	return strings.TrimSpace(payload), true
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
