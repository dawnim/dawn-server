package customer

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"

	"github.com/dawnim/dawn-server/app/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DecideAction 只确认已展示的工具及参数，客户端不能在确认时替换契约或身份。
func (m *Manager) DecideAction(ctx context.Context, id uint, session Session, adminID uint, approved bool) error {
	var run models.AIRun
	if err := m.DB.WithContext(ctx).Where("kind = ?", "tool").First(&run, id).Error; err != nil {
		return err
	}
	var actionMessage models.ChatMessage
	if err := m.DB.WithContext(ctx).First(&actionMessage, run.ReplyID).Error; err != nil {
		return err
	}
	if adminID == 0 && actionMessage.Visibility != "public" {
		return ErrForbidden
	}
	conv, err := m.Conversation(ctx, run.ConversationID, session, adminID, "reply")
	if err != nil {
		return err
	}
	if adminID != 0 && conv.AssignedAdminID != adminID {
		return ErrForbidden
	}
	if run.Version != conv.Version {
		return ErrConflict
	}
	var input struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
		Actor     ToolActor      `json:"actor"`
		Contract  string         `json:"contract"`
	}
	if json.Unmarshal(run.Input, &input) != nil {
		return ErrInvalid
	}
	_, cfg, _, err := m.App(ctx, run.AppID)
	if err != nil {
		return err
	}
	var tool Tool
	found := false
	for _, candidate := range cfg.Tools {
		if candidate.Name == input.Tool {
			tool = candidate
			found = true
			break
		}
	}
	if !found || TokenHash(string(Raw(tool))) != input.Contract {
		return ErrConflict
	}
	if adminID == 0 && ((tool.RequireIdentity && !session.Verified) || (tool.RequireAccount && (!session.Verified || session.User.Type != models.UserMember))) {
		return ErrUnauthenticated
	}
	lease := rand.Text()
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current models.ChatConversation
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, conv.ID).Error; e != nil {
			return e
		}
		if current.Version != run.Version {
			return ErrConflict
		}
		claim := tx.Model(&run).Where("status = ?", "waiting").Updates(map[string]any{"status": "running", "lease_token": lease})
		if claim.Error != nil {
			return claim.Error
		}
		if claim.RowsAffected != 1 {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return err
	}
	status := "cancelled"
	var output any
	if approved {
		input.Actor.AdminID = adminID
		output, err = m.ExecuteTool(ctx, input.Actor, tool, input.Arguments, true)
		status = "completed"
		if err != nil {
			status = "failed"
		}
	}
	saved := m.DB.WithContext(context.WithoutCancel(ctx)).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&run).Where("status = ? AND lease_token = ?", "running", lease).Updates(map[string]any{"status": status, "output": Raw(output)})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		var message models.ChatMessage
		if e := tx.First(&message, run.ReplyID).Error; e != nil {
			return e
		}
		var payload map[string]any
		_ = json.Unmarshal(message.Payload, &payload)
		payload["action_status"] = status
		payload["result"] = output
		message.Payload = Raw(payload)
		if e := tx.Model(&message).Update("payload", message.Payload).Error; e != nil {
			return e
		}
		return m.Event(tx, conv.ID, "chat.message.updated", message)
	})
	if saved == nil && m.Wake != nil {
		m.Wake(ctx)
	}
	return errors.Join(err, saved)
}
