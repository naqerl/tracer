package notify

import (
	"fmt"
	"time"

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
	var text string
	timeStr := n.Timestamp.Format(time.RFC3339)

	// Format differently based on notification type
	if n.Level == "STARTUP" {
		// Startup notification - compact and informative
		text = fmt.Sprintf(
			"🚀 **App Started**\n\n"+
				"**App:** `%s`\n"+
				"**Time:** %s\n",
			n.AppName,
			timeStr,
		)
		if n.Message != "" {
			text += fmt.Sprintf("\n**Message:**\n```\n%s\n```", n.Message)
		}
	} else {
		// Error/Alert notification - full details
		var emoji string
		switch n.Type {
		case TypeAlert:
			emoji = "⚠️"
		case TypeNotice:
			emoji = "ℹ️"
		default:
			emoji = "ℹ️"
		}

		text = fmt.Sprintf(
			"%s **%s**\n\n"+
				"**App:** `%s`\n"+
				"**Source:** `%s`\n"+
				"**Time:** %s\n",
			emoji,
			string(n.Type),
			n.AppName,
			n.Source,
			timeStr,
		)

		if n.TraceID != "" {
			text += fmt.Sprintf("**TraceID:** `%s`\n", n.TraceID)
		}

		text += fmt.Sprintf("\n**Message:**\n```\n%s\n```", n.Message)
	}

	// Use tgmd to convert standard markdown to Telegram MarkdownV2 format
	telegramText := tgmd.Telegramify(text)

	msg := tgbotapi.NewMessage(t.chatID, telegramText)
	msg.ParseMode = "MarkdownV2"

	_, err := t.bot.Send(msg)
	return err
}
