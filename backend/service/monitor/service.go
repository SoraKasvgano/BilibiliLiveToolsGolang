package monitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"bilibililivetools/gover/backend/store"
)

type RuntimeLog struct {
	Level   string    `json:"level"`
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
}

type Service struct {
	store  *store.Store
	buffer int

	mu   sync.RWMutex
	logs []RuntimeLog
}

func New(storeDB *store.Store, logBuffer int) *Service {
	if logBuffer <= 0 {
		logBuffer = 200
	}
	return &Service{
		store:  storeDB,
		buffer: logBuffer,
		logs:   make([]RuntimeLog, 0, logBuffer),
	}
}

func (s *Service) Infof(format string, args ...any) {
	s.logf("INFO", format, args...)
}

func (s *Service) Warnf(format string, args ...any) {
	s.logf("WARN", format, args...)
}

func (s *Service) Errorf(format string, args ...any) {
	s.logf("ERROR", format, args...)
}

func (s *Service) logf(level string, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = "INFO"
	}
	entry := RuntimeLog{
		Level:   level,
		Time:    time.Now().UTC(),
		Message: strings.TrimSpace(message),
	}
	s.mu.Lock()
	s.logs = append([]RuntimeLog{entry}, s.logs...)
	if len(s.logs) > s.buffer {
		s.logs = s.logs[:s.buffer]
	}
	s.mu.Unlock()

	switch level {
	case "ERROR":
		log.Printf("[monitor][error] %s", entry.Message)
	case "WARN":
		log.Printf("[monitor][warn] %s", entry.Message)
	default:
		log.Printf("[monitor] %s", entry.Message)
	}
}

func (s *Service) Logs(limit int) []RuntimeLog {
	if limit <= 0 {
		limit = 100
	}
	if limit > 2000 {
		limit = 2000
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.logs) < limit {
		limit = len(s.logs)
	}
	result := make([]RuntimeLog, limit)
	copy(result, s.logs[:limit])
	return result
}

func (s *Service) SendTestEmail(ctx context.Context, subject string, body string, receivers []string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	setting, err := s.store.GetMonitorSetting(ctx)
	if err != nil {
		s.logf("ERROR", "load monitor setting failed: %v", err)
		return nil, err
	}
	if !setting.IsEnableEmailNotice {
		err = errors.New("email notice is disabled")
		s.logf("WARN", "%s", err.Error())
		return nil, err
	}

	cleanReceivers := normalizeReceiverList(receivers)
	if len(cleanReceivers) == 0 {
		cleanReceivers = normalizeReceivers(setting.Receivers)
	}
	if len(cleanReceivers) == 0 {
		err = errors.New("no email receivers configured")
		s.logf("WARN", "%s", err.Error())
		return nil, err
	}

	cleanSubject := strings.TrimSpace(subject)
	if cleanSubject == "" {
		cleanSubject = "[Gover] Monitor test email"
	}
	cleanBody := strings.TrimSpace(body)
	if cleanBody == "" {
		cleanBody = "This is a monitor test email from Gover."
	}

	start := time.Now()
	if err := s.sendEmailSMTP(ctx, *setting, cleanSubject, cleanBody, cleanReceivers); err != nil {
		s.logf("ERROR", "send test email failed smtp=%s:%d recipients=%d err=%v",
			setting.SMTPServer, setting.SMTPPort, len(cleanReceivers), err)
		return nil, err
	}

	duration := time.Since(start)
	s.logf("INFO", "send test email succeeded smtp=%s:%d recipients=%d duration=%s",
		setting.SMTPServer, setting.SMTPPort, len(cleanReceivers), duration.String())
	return map[string]any{
		"smtpServer": setting.SMTPServer,
		"smtpPort":   setting.SMTPPort,
		"ssl":        setting.SMTPSsl,
		"from":       setting.MailAddress,
		"receivers":  cleanReceivers,
		"subject":    cleanSubject,
		"durationMs": duration.Milliseconds(),
	}, nil
}

func (s *Service) sendEmailSMTP(ctx context.Context, setting store.MonitorSetting, subject string, body string, receivers []string) error {
	host := strings.TrimSpace(setting.SMTPServer)
	if host == "" {
		return errors.New("smtpServer is required")
	}
	if setting.SMTPPort <= 0 {
		return errors.New("smtpPort is required")
	}
	from := strings.TrimSpace(setting.MailAddress)
	if from == "" {
		return errors.New("mailAddress is required")
	}
	if _, err := mail.ParseAddress(from); err != nil {
		return fmt.Errorf("mailAddress is invalid: %w", err)
	}
	cleanReceivers := normalizeReceiverList(receivers)
	if len(cleanReceivers) == 0 {
		return errors.New("receivers is required")
	}

	for _, receiver := range cleanReceivers {
		if _, err := mail.ParseAddress(receiver); err != nil {
			return fmt.Errorf("receiver is invalid: %s", receiver)
		}
	}

	var auth smtp.Auth
	username := strings.TrimSpace(setting.MailAddress)
	password := setting.Password
	if strings.TrimSpace(password) != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	serverAddr := net.JoinHostPort(host, fmt.Sprintf("%d", setting.SMTPPort))
	message := buildEmailMessage(setting, from, cleanReceivers, subject, body)
	sendFn := func(client *smtp.Client) error {
		defer client.Close()
		if auth != nil {
			if ok, _ := client.Extension("AUTH"); ok {
				if err := client.Auth(auth); err != nil {
					return err
				}
			}
		}
		if err := client.Mail(from); err != nil {
			return err
		}
		for _, receiver := range cleanReceivers {
			if err := client.Rcpt(receiver); err != nil {
				return err
			}
		}
		writer, err := client.Data()
		if err != nil {
			return err
		}
		if _, err := writer.Write(message); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		return client.Quit()
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if setting.SMTPSsl {
		conn, err := tls.DialWithDialer(dialer, "tcp", serverAddr, &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: false,
		})
		if err != nil {
			return err
		}
		client, err := smtp.NewClient(conn, host)
		if err != nil {
			_ = conn.Close()
			return err
		}
		return sendFn(client)
	}

	conn, err := dialer.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: false,
		}); err != nil {
			_ = client.Close()
			return err
		}
	}
	return sendFn(client)
}

func buildEmailMessage(setting store.MonitorSetting, from string, receivers []string, subject string, body string) []byte {
	displayName := strings.TrimSpace(setting.MailName)
	fromValue := from
	if displayName != "" {
		fromValue = (&mail.Address{Name: displayName, Address: from}).String()
	}
	subject = strings.TrimSpace(subject)
	if subject == "" {
		subject = "[Gover] Notification"
	}
	var buf bytes.Buffer
	buf.WriteString("From: " + fromValue + "\r\n")
	buf.WriteString("To: " + strings.Join(receivers, ",") + "\r\n")
	buf.WriteString("Subject: " + subject + "\r\n")
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	buf.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		buf.WriteString("\r\n")
	}
	return buf.Bytes()
}

func normalizeReceiverList(receivers []string) []string {
	if len(receivers) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(receivers))
	seen := map[string]struct{}{}
	for _, receiver := range receivers {
		trimmed := strings.TrimSpace(receiver)
		if trimmed == "" {
			continue
		}
		lower := strings.ToLower(trimmed)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}

func normalizeReceivers(raw string) []string {
	raw = strings.ReplaceAll(raw, "；", ";")
	raw = strings.ReplaceAll(raw, "，", ";")
	raw = strings.ReplaceAll(raw, ",", ";")
	parts := strings.Split(raw, ";")
	return normalizeReceiverList(parts)
}
