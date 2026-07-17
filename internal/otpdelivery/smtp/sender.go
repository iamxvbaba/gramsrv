package smtp

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	stdmail "net/mail"
	stdsmtp "net/smtp"
	"strings"
	"time"

	"telesrv/internal/otpdelivery"
)

type Config struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	TLSMode  string
	Timeout  time.Duration
}

type Sender struct {
	cfg Config
}

func New(cfg Config) *Sender {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	cfg.TLSMode = strings.ToLower(strings.TrimSpace(cfg.TLSMode))
	if cfg.TLSMode == "" {
		cfg.TLSMode = "starttls"
	}
	if strings.TrimSpace(cfg.From) == "" {
		cfg.From = cfg.Username
	}
	return &Sender{cfg: cfg}
}

func (s *Sender) Deliver(ctx context.Context, req otpdelivery.Request) (otpdelivery.Result, error) {
	if err := req.Validate(time.Now()); err != nil {
		return otpdelivery.Result{}, err
	}
	if req.Channel != otpdelivery.ChannelEmail {
		return otpdelivery.Result{}, fmt.Errorf("smtp cannot deliver channel %q", req.Channel)
	}
	ttl := time.Until(req.ExpiresAt)
	subject := "Your telesrv login code"
	body := fmt.Sprintf("Your telesrv login code is %s.\n\nThis code expires in %s. If you did not request it, ignore this email.\n", req.Code, humanTTL(ttl))
	if err := s.send(ctx, req.Recipient, subject, body); err != nil {
		return otpdelivery.Result{}, err
	}
	return otpdelivery.Result{}, nil
}

func (s *Sender) send(ctx context.Context, to, subject, body string) error {
	if strings.TrimSpace(s.cfg.Host) == "" {
		return fmt.Errorf("smtp host is empty")
	}
	from := strings.TrimSpace(s.cfg.From)
	if from == "" {
		return fmt.Errorf("smtp from is empty")
	}
	if _, err := stdmail.ParseAddress(to); err != nil {
		return fmt.Errorf("parse recipient: %w", err)
	}
	fromAddr := from
	if s.cfg.FromName != "" {
		fromAddr = (&stdmail.Address{Name: s.cfg.FromName, Address: from}).String()
	}
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	var d net.Dialer
	d.Timeout = s.cfg.Timeout
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	defer conn.Close()

	mode := strings.ToLower(strings.TrimSpace(s.cfg.TLSMode))
	var c *stdsmtp.Client
	if mode == "tls" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return fmt.Errorf("smtp tls handshake: %w", err)
		}
		c, err = stdsmtp.NewClient(tlsConn, s.cfg.Host)
	} else {
		c, err = stdsmtp.NewClient(conn, s.cfg.Host)
	}
	if err != nil {
		return fmt.Errorf("new smtp client: %w", err)
	}
	defer c.Close()
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
				return fmt.Errorf("smtp starttls: %w", err)
			}
		} else {
			return fmt.Errorf("smtp server does not support STARTTLS")
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(stdsmtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp rcpt: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp data: %w", err)
	}
	msg := buildMessage(fromAddr, to, subject, body)
	if _, err := w.Write(msg); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp close data: %w", err)
	}
	return c.Quit()
}

func buildMessage(from, to, subject, body string) []byte {
	var b bytes.Buffer
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(body)
	return b.Bytes()
}

func humanTTL(ttl time.Duration) string {
	if ttl <= 0 {
		return "a short time"
	}
	// Network and processing time can shave sub-second precision off an exact
	// configured TTL. Round up so a five-minute code is not rendered as 4m59s.
	ttl = ttl.Round(time.Second)
	if ttl%time.Minute == 0 {
		minutes := int(ttl / time.Minute)
		if minutes == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", minutes)
	}
	return ttl.String()
}
