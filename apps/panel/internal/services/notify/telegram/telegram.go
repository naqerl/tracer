package telegram

import (
	"encoding/json"
	"fmt"

	"github.com/ai-shift/tgmd"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"tracer/apps/panel/internal/services/notify"
)

// Config represents Telegram notifier configuration.
type Config struct {
	Token  string `json:"token"`
	ChatID int64  `json:"chat_id"`
}

// Notifier sends notifications via Telegram.
type Notifier struct {
	name   string
	bot    *tgbotapi.BotAPI
	chatID int64
}

// New creates a new Telegram notifier.
func New(name, token string, chatID int64) (*Notifier, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &Notifier{
		name:   name,
		bot:    bot,
		chatID: chatID,
	}, nil
}

// Type returns the notifier type.
func (t *Notifier) Type() string {
	return "telegram"
}

// Name returns the notifier name.
func (t *Notifier) Name() string {
	return t.name
}

// Notify sends a notification.
func (t *Notifier) Notify(n notify.Notification) error {
	// Message is already fully formatted by dispatcher
	// Just convert to Telegram MarkdownV2 format and send
	telegramText := tgmd.Telegramify(n.Message)

	msg := tgbotapi.NewMessage(t.chatID, telegramText)
	msg.ParseMode = "MarkdownV2"

	_, err := t.bot.Send(msg)
	return err
}

// FromRawJSON creates a Telegram notifier from config string (JSON).
func FromRawJSON(name, config string) (notify.Notifier, error) {
	var cfg Config
	if err := json.Unmarshal([]byte(config), &cfg); err != nil {
		return nil, fmt.Errorf("parse telegram config: %w", err)
	}
	return New(name, cfg.Token, cfg.ChatID)
}
