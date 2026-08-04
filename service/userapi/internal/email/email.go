// Package email 提供邮箱验证码生成与 SMTP 发送能力(QQ 邮箱 + gomail)。
package email

import (
	"crypto/tls"
	"fmt"
	"math/rand"
	"time"

	"gopkg.in/gomail.v2"
)

// GenerateCode 生成 6 位随机数字验证码
func GenerateCode() string {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	code := ""
	for i := 0; i < 6; i++ {
		code += fmt.Sprintf("%d", rng.Intn(10))
	}
	return code
}

// SendVerificationCode 通过 SMTP 发送验证码邮件
func SendVerificationCode(host string, port int, username, password, toEmail, code string) error {
	m := gomail.NewMessage()
	m.SetHeader("From", username)
	m.SetHeader("To", toEmail)
	m.SetHeader("Subject", "【Knovis】邮箱验证码")

	body := fmt.Sprintf(`
        <html>
        <body>
            <h2>欢迎使用 Knovis</h2>
            <p>您的验证码是：<strong style="color:red;font-size:24px;">%s</strong></p>
            <p>验证码5分钟内有效，请勿泄露。</p>
        </body>
        </html>
    `, code)

	m.SetBody("text/html", body)

	d := gomail.NewDialer(host, port, username, password)
	d.TLSConfig = &tls.Config{InsecureSkipVerify: true}

	return d.DialAndSend(m)
}
