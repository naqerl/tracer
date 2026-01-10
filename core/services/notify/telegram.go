package notify

import (
	"fmt"

	"github.com/ai-shift/tgmd"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// TelegramNotifier sends notifications via Telegram
type TelegramNotifier struct {
	name   string
	bot    *tgbotapi.BotAPI
	chatID int64
}

// NewTelegramNotifier creates a new Telegram notifier
func NewTelegramNotifier(name, token string, chatID int64) (*TelegramNotifier, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("create telegram bot: %w", err)
	}

	return &TelegramNotifier{
		name:   name,
		bot:    bot,
		chatID: chatID,
	}, nil
}

func (t *TelegramNotifier) Type() string {
	return "telegram"
}

func (t *TelegramNotifier) Name() string {
	return t.name
}

func (t *TelegramNotifier) Notify(n Notification) error {
	// Simple format: {level} @{app}\n{text}
	text := fmt.Sprintf("%s @%s\n%s", n.Level, n.AppName, n.Message)

	// Convert standard markdown to Telegram MarkdownV2 format
	telegramText := tgmd.Telegramify(text)

	msg := tgbotapi.NewMessage(t.chatID, telegramText)
	msg.ParseMode = "MarkdownV2"

	_, err := t.bot.Send(msg)
	return err
}
