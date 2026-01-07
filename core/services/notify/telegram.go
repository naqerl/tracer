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
	var text string

	// Format differently based on notification type
	if n.Level == "STARTUP" {
		// Startup notification - compact and informative
		text = fmt.Sprintf(
			"🚀 **App Started**\n"+
				"**App:** `%s`",
			n.AppName,
		)
		if n.Message != "" {
			text += fmt.Sprintf("\n**Message:** ```%s```", n.Message)
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
			"%s **%s**\n"+
				"**App:** `%s`",
			emoji,
			string(n.Type),
			n.AppName,
		)

		text += fmt.Sprintf("\n**Message:** ```%s```", n.Message)
	}

	// Add link to panel if panel URL is configured
	if n.PanelURL != "" {
		detailsURL := fmt.Sprintf("%s/logs/%s", n.PanelURL, n.AppName)
		text += fmt.Sprintf("\n[details](%s)", detailsURL)
	}

	// Use tgmd to convert standard markdown to Telegram MarkdownV2 format
	telegramText := tgmd.Telegramify(text)

	msg := tgbotapi.NewMessage(t.chatID, telegramText)
	msg.ParseMode = "MarkdownV2"

	_, err := t.bot.Send(msg)
	return err
}
