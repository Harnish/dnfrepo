package adminauth

import (
	gomail "github.com/go-mail/mail"

	"dnfrepo/internal/config"
)

// Mailer sends plain-text email.
type Mailer interface {
	Send(to, subject, body string) error
}

type smtpMailer struct {
	d    *gomail.Dialer
	from string
}

// NewSMTPMailer sends through the SMTP server named in cfg using go-mail/mail.
func NewSMTPMailer(cfg config.Config) Mailer {
	return &smtpMailer{d: gomail.NewDialer(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass), from: cfg.SMTPFrom}
}

func (m *smtpMailer) Send(to, subject, body string) error {
	msg := gomail.NewMessage()
	msg.SetHeader("From", m.from)
	msg.SetHeader("To", to)
	msg.SetHeader("Subject", subject)
	msg.SetBody("text/plain", body)
	return m.d.DialAndSend(msg)
}
