package smtp

import (
	"fmt"
	"html"
	"strings"
	"time"

	"telesrv/internal/branding"
	"telesrv/internal/otpdelivery"
)

// emailContent is one rendered code email: a subject and the same text as
// plain text and HTML.
type emailContent struct {
	Subject string
	Text    string
	HTML    string
}

// emailCopy is the wording of one email kind in one language.
type emailCopy struct {
	Heading string   // big line above the code
	Lines   []string // paragraphs below the code
}

// renderEmail builds the email for a delivery, worded like Telegram's own
// code emails, with the product name from branding. The request carries no
// reliable locale, so the email is bilingual: Russian first, then English.
func renderEmail(req otpdelivery.Request, ttl time.Duration) emailContent {
	product := branding.ProductName()
	if strings.TrimSpace(product) == "" {
		product = "ShuzaGram"
	}
	ru, en, subject := emailCopies(req.Purpose, product, ttl)
	subject = fmt.Sprintf(subject, req.Code)

	var text strings.Builder
	for i, c := range []emailCopy{ru, en} {
		if i > 0 {
			text.WriteString("\n----------------------------------------\n\n")
		}
		text.WriteString(c.Heading + "\n\n    " + req.Code + "\n\n")
		for _, line := range c.Lines {
			text.WriteString(line + "\n\n")
		}
	}
	text.WriteString(product + "\n")

	return emailContent{
		Subject: subject,
		Text:    text.String(),
		HTML:    renderHTML(product, req.Code, ru, en),
	}
}

// emailCopies returns the Russian and English wording and a subject format
// (with one %s for the code) for a purpose.
func emailCopies(purpose otpdelivery.Purpose, product string, ttl time.Duration) (emailCopy, emailCopy, string) {
	ruTTL, enTTL := ttlRU(ttl), humanTTL(ttl)
	switch purpose {
	case otpdelivery.PurposeLoginEmailSetup, otpdelivery.PurposeLoginEmailChange:
		return emailCopy{
				Heading: "Ваш код подтверждения почты в " + product + ":",
				Lines: []string{
					"Введите этот код в " + product + ", чтобы подтвердить, что этот адрес принадлежит вам. После этого его можно будет использовать для входа в аккаунт.",
					"Код действует " + ruTTL + ".",
					"Никому не сообщайте этот код, даже если вас просят об этом от имени " + product + ".",
					"Если вы не указывали эту почту в " + product + ", просто проигнорируйте это письмо.",
				},
			}, emailCopy{
				Heading: "Your " + product + " email verification code:",
				Lines: []string{
					"Enter this code in " + product + " to confirm that this address belongs to you. You will then be able to use it to log in to your account.",
					"This code expires in " + enTTL + ".",
					"Do not give this code to anyone, even if they say they are from " + product + ".",
					"If you did not add this email in " + product + ", simply ignore this message.",
				},
			}, product + " email verification code: %s"
	case otpdelivery.PurposePasswordRecovery:
		return emailCopy{
				Heading: "Код для сброса облачного пароля " + product + ":",
				Lines: []string{
					"Кто-то (надеемся, что вы) запросил сброс облачного пароля для вашего аккаунта " + product + ". Введите этот код в приложении, чтобы задать новый пароль.",
					"Код действует " + ruTTL + ".",
					"Никому не сообщайте этот код, даже если вас просят об этом от имени " + product + ". С ним можно получить доступ к вашему аккаунту.",
					"Если вы не запрашивали сброс пароля, просто проигнорируйте это письмо — ваш пароль останется прежним.",
				},
			}, emailCopy{
				Heading: "Your " + product + " password reset code:",
				Lines: []string{
					"Someone (hopefully you) asked to reset the Two-Step Verification password for your " + product + " account. Enter this code in the app to set a new password.",
					"This code expires in " + enTTL + ".",
					"Do not give this code to anyone, even if they say they are from " + product + ". It can be used to access your account.",
					"If you did not request a password reset, simply ignore this message — your password will not change.",
				},
			}, product + " password reset code: %s"
	default:
		return emailCopy{
				Heading: "Ваш код для входа в " + product + ":",
				Lines: []string{
					"Никому не сообщайте этот код, даже если вас просят об этом от имени " + product + "!",
					"С помощью этого кода можно войти в ваш аккаунт " + product + ". Мы никогда не просим его для чего-либо ещё.",
					"Код действует " + ruTTL + ".",
					"Если вы не пытались войти в " + product + " на другом устройстве, просто проигнорируйте это письмо.",
				},
			}, emailCopy{
				Heading: "Your " + product + " login code:",
				Lines: []string{
					"Do not give this code to anyone, even if they say they are from " + product + "!",
					"This code can be used to log in to your " + product + " account. We never ask for it for anything else.",
					"This code expires in " + enTTL + ".",
					"If you did not request this code by trying to log in on another device, simply ignore this message.",
				},
			}, product + " login code: %s"
	}
}

func renderHTML(product, code string, copies ...emailCopy) string {
	var b strings.Builder
	esc := html.EscapeString
	b.WriteString(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>` + esc(product) + `</title></head>
<body style="margin:0;padding:0;background:#f1f2f6;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#222;">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f1f2f6;padding:24px 12px;">
<tr><td align="center">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="max-width:520px;background:#ffffff;border-radius:14px;overflow:hidden;">
<tr><td style="background:#4a5068;padding:22px 28px;color:#ffffff;font-size:22px;font-weight:700;">` + esc(product) + `</td></tr>
`)
	for i, c := range copies {
		if i > 0 {
			b.WriteString(`<tr><td style="padding:0 28px;"><hr style="border:0;border-top:1px solid #e3e5ec;margin:4px 0;"></td></tr>
`)
		}
		b.WriteString(`<tr><td style="padding:24px 28px 8px;font-size:16px;line-height:1.5;">` + esc(c.Heading) + `</td></tr>
<tr><td align="center" style="padding:8px 28px 16px;"><div style="display:inline-block;padding:14px 26px;border-radius:12px;background:#eef0f6;font-size:34px;font-weight:700;letter-spacing:8px;color:#222;">` + esc(code) + `</div></td></tr>
`)
		for _, line := range c.Lines {
			b.WriteString(`<tr><td style="padding:0 28px 12px;font-size:14px;line-height:1.55;color:#555;">` + esc(line) + `</td></tr>
`)
		}
	}
	b.WriteString(`<tr><td style="padding:12px 28px 24px;font-size:12px;color:#999;">` + esc(product) + `</td></tr>
</table>
</td></tr>
</table>
</body></html>
`)
	return b.String()
}

// ttlRU renders a code lifetime in Russian ("5 минут").
func ttlRU(ttl time.Duration) string {
	if ttl <= 0 {
		return "недолго"
	}
	ttl = ttl.Round(time.Second)
	if ttl%time.Minute != 0 {
		return ttl.String()
	}
	n := int(ttl / time.Minute)
	switch {
	case n%10 == 1 && n%100 != 11:
		return fmt.Sprintf("%d минуту", n)
	case n%10 >= 2 && n%10 <= 4 && (n%100 < 12 || n%100 > 14):
		return fmt.Sprintf("%d минуты", n)
	default:
		return fmt.Sprintf("%d минут", n)
	}
}
