package controllers

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/url"

	"github.com/dawnim/dawn-server/app/admin"
	"github.com/dawnim/dawn-server/app/admin/middleware"
	"github.com/dawnim/dawn-server/app/admin/response"
	"github.com/dawnim/dawn-server/app/models"
	"github.com/dawnim/dawn-server/constants"
	"github.com/gin-gonic/gin"
)

type linkSession struct {
	System  string `json:"system"`
	Session string `json:"session"`
	Menu    string `json:"menu"`
}

func (a *Controller) LinkRoutes(g *gin.RouterGroup) {
	g.Match([]string{"GET", "POST"}, "/links/:key/launch", middleware.Require(a.App, ""), func(c *gin.Context) {
		var link models.Menu
		if err := a.DB.Where(map[string]any{"key": c.Param("key"), "type": models.MenuLink, "status": models.Enabled}).First(&link).Error; err != nil {
			response.Report(c, err)
			return
		}
		if !middleware.Actor(c).Can(link.Key) {
			response.Fail(c, 403, "没有外链访问权限")
			return
		}
		if err := a.ValidateMenu(&link, true); err != nil {
			response.Report(c, err, 422)
			return
		}
		target, err := url.Parse(link.URL)
		if err != nil {
			response.Report(c, err)
			return
		}
		if link.SystemKey != "" {
			ticket := rand.Text()
			data, err := json.Marshal(linkSession{System: link.SystemKey, Session: middleware.Actor(c).SessionID, Menu: link.Key})
			if err != nil {
				response.Report(c, err)
				return
			}
			if err := a.Redis.Set(c.Request.Context(), a.Key(constants.TempLinkTicket, admin.TokenHash(ticket)), data, constants.TempLinkTicket.TTL).Err(); err != nil {
				response.Report(c, err)
				return
			}
			query := target.Query()
			query.Set("dawn_ticket", ticket)
			target.RawQuery = query.Encode()
		}
		if c.Request.Method == "GET" {
			c.Redirect(302, target.String())
			return
		}
		response.Respond(c, gin.H{"url": target.String()})
	})
	for _, action := range []string{"exchange", "authorize"} {
		g.POST("/integrations/"+action, func(c *gin.Context) {
			name, secret, ok := c.Request.BasicAuth()
			var system *admin.LinkSystem
			for i := range a.Options.LinkSystems {
				candidate := &a.Options.LinkSystems[i]
				if candidate.Key == name && len(candidate.Secret) >= 32 && subtle.ConstantTimeCompare([]byte(admin.TokenHash(secret)), []byte(admin.TokenHash(candidate.Secret))) == 1 {
					system = candidate
				}
			}
			if !ok || system == nil {
				response.Fail(c, 401, "外部系统认证失败")
				return
			}
			var input struct {
				Ticket     string `json:"ticket"`
				Token      string `json:"access_token"`
				Permission string `json:"permission"`
			}
			if c.ShouldBindJSON(&input) != nil {
				response.Fail(c, 422, "参数格式错误")
				return
			}
			key := a.Key(constants.TempLinkSession, admin.TokenHash(input.Token))
			if action == "exchange" {
				key = a.Key(constants.TempLinkTicket, admin.TokenHash(input.Ticket))
			}
			raw, err := a.Redis.Get(c.Request.Context(), key).Bytes()
			if err != nil {
				response.Fail(c, 401, "接入凭证已失效")
				return
			}
			var state linkSession
			if err := json.Unmarshal(raw, &state); err != nil {
				response.Report(c, err)
				return
			}
			if state.System != system.Key {
				response.Fail(c, 403, "凭证不属于该系统")
				return
			}
			current, err := a.ActorForSession(state.Session)
			if err != nil {
				response.Fail(c, 401, "管理员会话已失效")
				return
			}
			if !current.Can(state.Menu) {
				response.Fail(c, 403, "外链授权已撤销")
				return
			}
			var count int64
			if err := a.DB.Model(&models.Menu{}).Where(map[string]any{"key": state.Menu, "system_key": system.Key, "type": models.MenuLink, "status": models.Enabled}).Count(&count).Error; err != nil {
				response.Report(c, err)
				return
			}
			if count == 0 {
				response.Fail(c, 403, "外链已移除")
				return
			}
			permissions := []string{}
			allowed := input.Permission == ""
			for _, permission := range system.Permissions {
				if current.Can(permission) {
					permissions = append(permissions, permission)
					if permission == input.Permission {
						allowed = true
					}
				}
			}
			result := gin.H{"user_id": current.User.ID, "name": current.User.Realname, "permissions": permissions, "allowed": allowed}
			if action == "exchange" {
				used, err := a.Redis.GetDel(c.Request.Context(), key).Bytes()
				if err != nil || string(used) != string(raw) {
					response.Fail(c, 401, "票据已经使用")
					return
				}
				token := rand.Text()
				if err := a.Redis.Set(c.Request.Context(), a.Key(constants.TempLinkSession, admin.TokenHash(token)), raw, constants.TempLinkSession.TTL).Err(); err != nil {
					response.Report(c, err)
					return
				}
				result["access_token"] = token
				result["expires_in"] = int(constants.TempLinkSession.TTL.Seconds())
			}
			response.Respond(c, result)
		})
	}
}
