package controllers

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"time"

	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/support/i18n"
	"github.com/gin-gonic/gin"
)

func (a *Controller) MailRoutes(g *gin.RouterGroup) {
	g.POST("/system/mail/test", middleware.Require(a.App, "dawn.settings.update"), func(c *gin.Context) {
		var input struct {
			To string `json:"to" binding:"required,email"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Fail(c, 422, "收件地址格式错误")
			return
		}
		values := map[string]string{}
		for _, key := range []string{"mail.host", "mail.port", "mail.username", "mail.password", "mail.security", "mail.from", "mail.from_name"} {
			raw, err := a.Setting(c.Request.Context(), key)
			if err != nil {
				response.Report(c, err)
				return
			}
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				response.Report(c, err)
				return
			}
			values[key] = fmt.Sprint(value)
		}
		from := &mail.Address{Name: values["mail.from_name"], Address: values["mail.from"]}
		message := "From: " + from.String() + "\r\nTo: " + input.To + "\r\nSubject: " + mime.QEncoding.Encode("UTF-8", i18n.Text(c.GetString("admin.locale"), "Dawn邮件测试")) + "\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" + i18n.Text(c.GetString("admin.locale"), "Dawn邮件测试") + "\r\n"
		c.Set("admin.mail.sender", values["mail.from"])
		c.Set("admin.mail.recipient", input.To)
		err := sendMail(c.Request.Context(), values, input.To, []byte(message))
		if err != nil {
			middleware.RecordAudit(a.DB, c, "email", 500, "测试邮件发送失败")
			response.Report(c, err)
			return
		}
		middleware.RecordAudit(a.DB, c, "email", 200, "测试邮件已发送")
		response.Respond(c, nil)
	})
}

// sendMail由net/smtp处理协议，TLS与STARTTLS分别显式握手。
func sendMail(ctx context.Context, v map[string]string, to string, message []byte) error {
	if _, err := mail.ParseAddress(v["mail.from"]); err != nil {
		return err
	}
	if _, err := strconv.Atoi(v["mail.port"]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	addr := net.JoinHostPort(v["mail.host"], v["mail.port"])
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { connection.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	tlsConfig := &tls.Config{ServerName: v["mail.host"], MinVersion: tls.VersionTLS12}
	if v["mail.security"] == "ssl" {
		secure := tls.Client(connection, tlsConfig)
		if err := secure.HandshakeContext(ctx); err != nil {
			return err
		}
		connection = secure
	}
	client, err := smtp.NewClient(connection, v["mail.host"])
	if err != nil {
		return err
	}
	defer client.Close()
	if v["mail.security"] == "tls" {
		if err := client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if v["mail.username"] != "" {
		if err := client.Auth(smtp.PlainAuth("", v["mail.username"], v["mail.password"], v["mail.host"])); err != nil {
			return err
		}
	}
	if err := client.Mail(v["mail.from"]); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := writer.Write(message); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}
