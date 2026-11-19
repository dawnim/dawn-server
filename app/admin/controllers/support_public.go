package controllers

import (
	"cmp"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/dawnim/dawn-server/realtime"
	"github.com/dawnim/dawn-server/support/customer"
	"github.com/dawnim/dawn-server/support/geoip"
	"github.com/gin-gonic/gin"
	"github.com/prismgo/framework/config"
	"github.com/prismgo/framework/route"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// customerSession 将公开请求限制到当前入口，不接受客户端提交的内部用户ID。
func (a *Controller) customerSession(c *gin.Context) (customer.Session, bool) {
	var app models.SupportApp
	if err := a.DB.WithContext(c.Request.Context()).Where("public_key = ? AND status = ?", c.Param("key"), models.Enabled).First(&app).Error; err != nil {
		response.Report(c, err)
		return customer.Session{}, false
	}
	s, err := a.Customer.Authenticate(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), app.ID)
	if err != nil {
		response.Report(c, err)
		return s, false
	}
	return s, true
}

func (a *Controller) SupportPublicRoutes() {
	route.Get("/chat/{key}", func(c *gin.Context) {
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Cache-Control", "no-store")
		data, _ := customer.Web.ReadFile("web/chat.html")
		html := string(data)
		for _, name := range []string{"chat.css", "chat.js", "markdown.css", "markdown.js"} {
			asset, _ := customer.Web.ReadFile("web/" + name)
			html = strings.ReplaceAll(html, "/support-assets/"+name, fmt.Sprintf("/support-assets/%s?v=%x", name, sha256.Sum256(asset)))
		}
		c.Data(200, "text/html; charset=utf-8", []byte(html))
	})
	route.Get("/support-assets/{name}", func(c *gin.Context) {
		name := c.Param("name")
		if !slices.Contains([]string{"chat.js", "chat.css", "widget.js", "markdown.js", "markdown.css", "markdown.LICENSE"}, name) {
			c.Status(404)
			return
		}
		data, err := customer.Web.ReadFile("web/" + name)
		if err != nil {
			c.Status(404)
			return
		}
		kind := "text/javascript; charset=utf-8"
		if strings.HasSuffix(name, ".css") {
			kind = "text/css; charset=utf-8"
		} else if name == "markdown.LICENSE" {
			kind = "text/plain; charset=utf-8"
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Cache-Control", "public, max-age=300")
		c.Data(200, kind, data)
	})
	throttle := func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 256<<10)
		if c.Request.Method != "POST" {
			return
		}
		key := a.Key(constants.TempSupportRate, customer.TokenHash(c.ClientIP()+":"+c.FullPath()))
		count, err := a.Redis.Eval(c.Request.Context(), `local n=redis.call('INCR',KEYS[1]);if n==1 then redis.call('EXPIRE',KEYS[1],60) end;return n`, []string{key}).Int()
		if err != nil {
			response.Report(c, err)
			return
		}
		limit := 120
		if strings.HasPrefix(c.Request.URL.Path, "/api/account") {
			limit = 10
		}
		if count > limit {
			response.Report(c, customer.ErrLimited)
		}
	}
	g := route.Prefix("/api/support").Middleware(throttle)
	g.Put("/{key}/actions/{id}", func(c *gin.Context) {
		session, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			Approved bool `json:"approved"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.DecideAction(c.Request.Context(), id, session, 0, input.Approved); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})

	g.Get("/{key}/configuration", func(c *gin.Context) {
		var app models.SupportApp
		if err := a.DB.WithContext(c.Request.Context()).Where("public_key = ? AND status = ?", c.Param("key"), models.Enabled).First(&app).Error; err != nil {
			response.Report(c, err)
			return
		}
		var cfg customer.Config
		if err := json.Unmarshal(app.Config, &cfg); err != nil {
			response.Report(c, err)
			return
		}
		entry := customer.Entry{Key: "default", Theme: "system", Greeting: cfg.Greeting}
		for _, item := range cfg.Entries {
			if item.Key == c.DefaultQuery("entry", "default") {
				entry = item
				break
			}
		}
		response.Respond(c, gin.H{"name": app.Name, "avatar": app.Avatar, "greeting": cmp.Or(entry.Greeting, cfg.Greeting), "theme": entry.Theme, "origins": entry.Origins, "input_preview": cfg.InputPreview})
	})
	g.Patch("/{key}/context", func(c *gin.Context) {
		session, ok := a.customerSession(c)
		if !ok {
			return
		}
		var input struct {
			Attributes map[string]any `json:"attributes"`
			Observed   map[string]any `json:"observed"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		var traits models.UserTraits
		err := a.DB.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("app_id = ? AND user_id = ?", session.AppID, session.UserID).First(&traits).Error; err != nil {
				return err
			}
			observed := map[string]any{}
			_ = json.Unmarshal(traits.Observed, &observed)
			for _, key := range []string{"page_url", "page_title", "referrer", "page_origin", "utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"} {
				if value, exists := input.Observed[key]; exists {
					observed[key] = value
				}
			}
			attributes := map[string]any{}
			_ = json.Unmarshal(traits.Attributes, &attributes)
			for key, value := range input.Attributes {
				attributes[key] = value
			}
			return tx.Model(&traits).Updates(map[string]any{"attributes": customer.Raw(attributes), "observed": customer.Raw(observed), "last_seen_at": time.Now().UnixMilli()}).Error
		})
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	for _, resource := range []string{"sessions", "identities"} {
		g.Post("/{key}/"+resource, func(c *gin.Context) {
			var input customer.OpenInput
			if c.ShouldBindJSON(&input) != nil {
				response.Report(c, customer.ErrInvalid)
				return
			}
			if input.Observed == nil {
				input.Observed = map[string]any{}
			}
			input.Observed["ip"] = c.ClientIP()
			input.Observed["user_agent"] = c.Request.UserAgent()
			input.Observed["seen_at"] = time.Now().UnixMilli()
			if location, err := geoip.Lookup(c.Request.Context(), c.ClientIP()); err == nil {
				input.Observed["geo_ip"] = location
			}
			if resource == "identities" {
				session, err := a.Customer.Identify(c.Request.Context(), c.Param("key"), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), input)
				if err != nil {
					response.Report(c, err)
					return
				}
				response.Created(c, session, c.Request.URL.Path)
				return
			}
			session, conv, err := a.Customer.Open(c.Request.Context(), c.Param("key"), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), input)
			if err != nil {
				response.Report(c, err)
				return
			}
			response.Created(c, gin.H{"session": session, "conversation": conv}, "/api/support/"+c.Param("key")+"/conversations/"+strconv.FormatUint(uint64(conv.ID), 10))
		})
	}

	g.Post("/{key}/visitor-tickets", func(c *gin.Context) {
		session, ok := a.customerSession(c)
		if !ok {
			return
		}
		ticket, err := a.Customer.VisitorTicket(c.Request.Context(), session)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, gin.H{"ticket": ticket}, c.Request.URL.Path)
	})
	g.Post("/{key}/visitor-ticket-exchanges", func(c *gin.Context) {
		var input struct {
			Ticket string `json:"ticket"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		visitor, err := a.Customer.ExchangeVisitor(c.Request.Context(), c.Param("key"), input.Ticket, strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, visitor, c.Request.URL.Path)
	})
	g.Delete("/{key}/session", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		if err := a.Customer.Sessions.Revoke(c.Request.Context(), s.ID); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(204)
	})
	g.Post("/{key}/realtime-tickets", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		var input struct {
			Resume string `json:"resume"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		ticket, err := a.Realtime.Ticket(c.Request.Context(), realtime.Principal{Realm: customer.Realm(s.AppID), Subject: s.User.UniqueID, SessionID: s.ID}, input.Resume, c.GetString("admin.locale"))
		if err != nil {
			response.Report(c, err)
			return
		}
		wsURL := config.GetString("dawn.ws_url")
		if wsURL == "" {
			_, port, _ := net.SplitHostPort(a.Options.WSAddr)
			scheme := "ws"
			if c.Request.TLS != nil {
				scheme = "wss"
			}
			wsURL = (&url.URL{Scheme: scheme, Host: net.JoinHostPort((&url.URL{Host: c.Request.Host}).Hostname(), port), Path: a.Options.WSPath}).String()
		}
		response.Created(c, gin.H{"ticket": ticket, "ws_url": wsURL}, "/api/support/"+c.Param("key"))
	})
	g.Get("/{key}/conversations", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		page, size := response.Page(c)
		q := a.DB.WithContext(c.Request.Context()).Model(&models.ChatConversation{}).Where("type = ? AND app_id = ? AND user_id = ?", "support", s.AppID, s.UserID)
		var total int64
		if err := q.Count(&total).Error; err != nil {
			response.Report(c, err)
			return
		}
		rows := []models.ChatConversation{}
		if err := q.Order("last_message_id DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Paginated(c, rows, total, page, size)
	})
	g.Get("/{key}/conversations/{id}/messages", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		_, size := response.Page(c)
		before, _ := strconv.ParseUint(c.Query("before_id"), 10, 64)
		rows, more, err := a.Customer.History(c.Request.Context(), id, s, 0, uint(before), size)
		if err != nil {
			response.Report(c, err)
			return
		}
		var next uint
		if len(rows) > 0 {
			next = rows[len(rows)-1].ID
		}
		c.JSON(200, gin.H{"data": rows, "meta": gin.H{"has_more": more, "before_id": next}})
	})
	g.Get("/{key}/conversations/{id}", func(c *gin.Context) {
		visitor, ok := a.customerSession(c)
		if !ok {
			return
		}
		conv, err := a.Customer.Conversation(c.Request.Context(), response.ID(c), visitor, 0, "")
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, conv)
	})
	g.Post("/{key}/conversations/{id}/endings", func(c *gin.Context) {
		visitor, ok := a.customerSession(c)
		if !ok {
			return
		}
		var input struct {
			SessionID uint `json:"session_id"`
			Version   uint `json:"version"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		conv, err := a.Customer.DecideClosure(c.Request.Context(), response.ID(c), visitor, input.SessionID, input.Version, true)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, conv, c.Request.URL.Path)
	})
	g.Put("/{key}/conversations/{id}/closing-request", func(c *gin.Context) {
		visitor, ok := a.customerSession(c)
		if !ok {
			return
		}
		var input struct {
			SessionID uint  `json:"session_id"`
			Version   uint  `json:"version"`
			Approved  *bool `json:"approved"`
		}
		if c.ShouldBindJSON(&input) != nil || input.Approved == nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		conv, err := a.Customer.DecideClosure(c.Request.Context(), response.ID(c), visitor, input.SessionID, input.Version, *input.Approved)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, conv)
	})
	g.Get("/{key}/conversations/{id}/support-sessions/{session_id}", func(c *gin.Context) {
		visitor, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if _, err := a.Customer.Conversation(c.Request.Context(), id, visitor, 0, ""); err != nil {
			response.Report(c, err)
			return
		}
		var row models.SupportSession
		if err := a.DB.WithContext(c.Request.Context()).Where("id = ? AND conversation_id = ?", c.Param("session_id"), id).First(&row).Error; err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, row)
	})
	g.Post("/{key}/conversations/{id}/messages/{message_id}/retries", func(c *gin.Context) {
		visitor, ok := a.customerSession(c)
		if !ok {
			return
		}
		messageID, e := strconv.ParseUint(c.Param("message_id"), 10, 64)
		if e != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if e = a.Customer.RetryReply(c.Request.Context(), response.ID(c), visitor, uint(messageID)); e != nil {
			response.Report(c, e)
			return
		}
		var reply models.ChatMessage
		if e := a.DB.WithContext(c.Request.Context()).Where("conversation_id = ?", response.ID(c)).First(&reply, uint(messageID)).Error; e != nil {
			response.Report(c, e)
			return
		}
		response.Created(c, reply, strings.TrimSuffix(c.Request.URL.Path, "/retries"))
	})
	g.Post("/{key}/conversations/{id}/messages", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			Content   string `json:"content"`
			RequestID string `json:"request_id"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		message, err := a.Customer.Send(c.Request.Context(), id, s, 0, customer.MessageInput{RequestID: input.RequestID, Content: input.Content, Visibility: "public"})
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, message, c.Request.URL.Path+"/"+strconv.FormatUint(uint64(message.ID), 10))
	})
	g.Put("/{key}/conversations/{id}/read-position", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			MessageID uint `json:"message_id"`
		}
		if c.ShouldBindJSON(&input) != nil || input.MessageID == 0 {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.Read(c.Request.Context(), id, s, 0, input.MessageID); err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, nil)
	})
	g.Post("/{key}/conversations/{id}/human-requests", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		conv, err := a.Customer.Reception(c.Request.Context(), id, s, 0, 0, 0, "human")
		if err != nil {
			response.Report(c, err)
			return
		}
		_ = a.Customer.AssignWaiting(c.Request.Context())
		response.Created(c, conv, "/api/support/"+c.Param("key")+"/conversations/"+strconv.FormatUint(uint64(id), 10))
	})
	g.Post("/{key}/conversations/{id}/ratings", func(c *gin.Context) {
		s, ok := a.customerSession(c)
		if !ok {
			return
		}
		id := response.ID(c)
		if id == 0 {
			return
		}
		var input struct {
			SessionID uint   `json:"session_id"`
			Version   uint   `json:"version"`
			Rating    int    `json:"rating"`
			Comment   string `json:"comment"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		if err := a.Customer.Rate(c.Request.Context(), id, s, input.SessionID, input.Version, input.Rating, input.Comment); err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, input, c.Request.URL.Path)
	})
	account := route.Prefix("/api/account").Middleware(throttle)
	account.Post("/registrations", func(c *gin.Context) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
			Nickname string `json:"nickname"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		var current customer.Session
		if token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "); token != "" {
			var err error
			current, err = a.Customer.Authenticate(c.Request.Context(), token, 0)
			if err != nil {
				response.Report(c, err)
				return
			}
		}
		s, err := a.Customer.Register(c.Request.Context(), current, input.Username, input.Password, input.Nickname)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, s, "/api/account/profile")
	})
	account.Post("/sessions", func(c *gin.Context) {
		var input struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		s, err := a.Customer.Login(c.Request.Context(), input.Username, input.Password)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, s, "/api/account/profile")
	})
	account.Get("/profile", func(c *gin.Context) {
		s, err := a.Customer.Authenticate(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), 0)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Respond(c, s.User.Profile())
	})
	account.Delete("/session", func(c *gin.Context) {
		s, err := a.Customer.Authenticate(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), 0)
		if err != nil {
			response.Report(c, err)
			return
		}
		if err = a.Customer.Sessions.Revoke(c.Request.Context(), s.ID); err != nil {
			response.Report(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	})
	account.Post("/support-tickets", func(c *gin.Context) {
		s, err := a.Customer.Authenticate(c.Request.Context(), strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "), 0)
		if err != nil {
			response.Report(c, err)
			return
		}
		var input struct {
			AppID uint `json:"app_id"`
		}
		if c.ShouldBindJSON(&input) != nil {
			response.Report(c, customer.ErrInvalid)
			return
		}
		ticket, err := a.Customer.IdentityTicket(c.Request.Context(), s.UserID, input.AppID)
		if err != nil {
			response.Report(c, err)
			return
		}
		response.Created(c, gin.H{"ticket": ticket}, c.Request.URL.Path)
	})
}
