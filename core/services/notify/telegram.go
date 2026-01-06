package notify

import (
	"fmt"
	"time"

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
	var emoji string
	switch n.Type {
	case TypeAlert:
		emoji = "\\u26A0\\uFE0F" // Warning sign
	case TypeNotice:
		emoji = "\\u2139\\uFE0F" // Info sign
	default:
		emoji = "\\u2139\\uFE0F"
	}

	text := fmt.Sprintf(
		"%s *%s*\n\n"+
			"*App:* `%s`\n"+
			"*Source:* `%s`\n"+
			"*Time:* %s\n",
		emoji,
		escapeMarkdown(string(n.Type)),
		escapeMarkdown(n.AppName),
		escapeMarkdown(n.Source),
		n.Timestamp.Format(time.RFC3339),
	)

	if n.TraceID != "" {
		text += fmt.Sprintf("*TraceID:* `%s`\n", escapeMarkdown(n.TraceID))
	}

	text += fmt.Sprintf("\n*Message:*\n```\n%s\n```", escapeMarkdown(n.Message))

	msg := tgbotapi.NewMessage(t.chatID, text)
	msg.ParseMode = "MarkdownV2"

	_, err := t.bot.Send(msg)
	return err
}

func escapeMarkdown(s string) string {
	replacer := []string{
		"_", "\\_",
		"*", "\\*",
		"[", "\\[",
		"]", "\\]",
		"(", "\\(",
		")", "\\)",
		"~", "\\~",
		"`", "\\`",
		">", "\\>",
		"#", "\\#",
		"+", "\\+",
		"-", "\\-",
		"=", "\\=",
		"|", "\\|",
		"{", "\\{",
		"}", "\\}",
		".", "\\.",
		"!", "\\!",
	}
	result := s
	for i := 0; i < len(replacer); i += 2 {
		result = replaceAll(result, replacer[i], replacer[i+1])
	}
	return result
}

func replaceAll(s, old, new string) string {
	result := ""
	for i := 0; i < len(s); i++ {
		if string(s[i]) == old {
			result += new
		} else {
			result += string(s[i])
		}
	}
	return result
}
