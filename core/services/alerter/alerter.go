package alerter

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const maxWorkers = 5

// Alert represents an error alert to be sent
type Alert struct {
	AppName   string
	TraceID   string
	Level     string
	Message   string
	Source    string
	Timestamp time.Time
}

type Service struct {
	AlertChan chan Alert
	semaphore chan struct{}
	bot       *tgbotapi.BotAPI
	chatID    int64
	enabled   bool
}

func New(token string, chatID int64) (*Service, error) {
	s := &Service{
		AlertChan: make(chan Alert, 100),
		semaphore: make(chan struct{}, maxWorkers),
		chatID:    chatID,
	}

	if token == "" || chatID == 0 {
		slog.Info("Telegram alerter disabled (no token or chat_id)")
		s.enabled = false
		return s, nil
	}

	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	s.bot = bot
	s.enabled = true
	slog.Info("Telegram alerter enabled", "bot_username", bot.Self.UserName)

	return s, nil
}

func (s *Service) Start(ctx context.Context) {
	slog.Info("Alerter service started", "enabled", s.enabled)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Alerter service stopping")
			return
		case alert := <-s.AlertChan:
			if !s.enabled {
				continue
			}
			s.semaphore <- struct{}{}
			go func(a Alert) {
				defer func() { <-s.semaphore }()
				s.sendAlert(ctx, a)
			}(alert)
		}
	}
}

func (s *Service) sendAlert(ctx context.Context, alert Alert) {
	text := fmt.Sprintf(
		"*Error Alert*\n\n"+
			"*App:* `%s`\n"+
			"*Source:* `%s`\n"+
			"*Time:* %s\n",
		escapeMarkdown(alert.AppName),
		escapeMarkdown(alert.Source),
		alert.Timestamp.Format(time.RFC3339),
	)

	if alert.TraceID != "" {
		text += fmt.Sprintf("*TraceID:* `%s`\n", escapeMarkdown(alert.TraceID))
	}

	text += fmt.Sprintf("\n*Message:*\n```\n%s\n```", escapeMarkdown(alert.Message))

	msg := tgbotapi.NewMessage(s.chatID, text)
	msg.ParseMode = "MarkdownV2"

	if _, err := s.bot.Send(msg); err != nil {
		slog.Error("Failed to send Telegram alert", "error", err, "app", alert.AppName)
	}
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
