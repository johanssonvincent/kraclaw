package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/johanssonvincent/kraclaw/internal/ipc"
	"github.com/johanssonvincent/kraclaw/internal/store"
)

const commandPrefix = "/"

func (o *Orchestrator) handleSlashCommand(ctx context.Context, chatJID string, msgContent string, sender string) bool {
	content := strings.TrimSpace(msgContent)
	if !strings.HasPrefix(content, commandPrefix) {
		return false
	}
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return false
	}

	cmd := strings.TrimPrefix(fields[0], commandPrefix)
	cmd = strings.ToLower(cmd)

	allowed, err := o.auth.IsAllowed(ctx, chatJID, sender)
	if err != nil {
		o.log.Error("command allowlist check failed", "chat_jid", chatJID, "error", err)
		o.sendSystemMessage(ctx, chatJID, "Unable to check permissions for this command.")
		return true
	}
	if !allowed {
		o.sendSystemMessage(ctx, chatJID, "You are not allowed to run commands in this chat.")
		return true
	}

	switch cmd {
	case "models":
		o.handleModelsCommand(ctx, chatJID)
		return true
	case "model":
		arg := ""
		if len(fields) > 1 {
			arg = fields[1]
		}
		o.handleModelCommand(ctx, chatJID, arg)
		return true
	case "commands", "help":
		o.sendSystemMessage(ctx, chatJID, "Available commands:\n- /models\n- /model <id>\n- /model\n- /help")
		return true
	default:
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Unknown command: /%s", cmd))
		return true
	}
}

func (o *Orchestrator) handleModelsCommand(ctx context.Context, chatJID string) {
	models, stale, err := o.models.List(ctx)
	currentModel, currentLabel, errGroup := o.currentModelForChat(ctx, chatJID)
	if errGroup != nil {
		o.log.Error("failed to get group for /models", "chat_jid", chatJID, "error", errGroup)
		o.sendSystemMessage(ctx, chatJID, "Unable to fetch current model for this chat.")
		return
	}

	if len(models) == 0 && err != nil {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Failed to fetch models: %v", err))
		return
	}

	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})

	var b strings.Builder
	if err != nil {
		b.WriteString("Models (cached; upstream error):\n")
	} else if stale {
		b.WriteString("Models (cached):\n")
	} else {
		b.WriteString("Models:\n")
	}

	if len(models) == 0 {
		b.WriteString("(no models returned)")
		if currentLabel != "" {
			b.WriteString("\nCurrent: ")
			b.WriteString(currentLabel)
		}
		o.sendSystemMessage(ctx, chatJID, b.String())
		return
	}

	for _, m := range models {
		name := m.ID
		if m.DisplayName != "" {
			name = fmt.Sprintf("%s (%s)", m.ID, m.DisplayName)
		}
		marker := ""
		if currentModel != "" && m.ID == currentModel {
			marker = " (current)"
		}
		b.WriteString("- ")
		b.WriteString(name)
		b.WriteString(marker)
		b.WriteString("\n")
	}
	if currentModel == "" && currentLabel != "" {
		b.WriteString("Current: ")
		b.WriteString(currentLabel)
	}

	o.sendSystemMessage(ctx, chatJID, strings.TrimRight(b.String(), "\n"))
}

func (o *Orchestrator) handleModelCommand(ctx context.Context, chatJID string, requested string) {
	currentModel, currentLabel, err := o.currentModelForChat(ctx, chatJID)
	if err != nil {
		o.log.Error("failed to get group for /model", "chat_jid", chatJID, "error", err)
		o.sendSystemMessage(ctx, chatJID, "Unable to fetch current model for this chat.")
		return
	}

	if requested == "" {
		if currentLabel == "" {
			currentLabel = "default"
		}
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Current model: %s", currentLabel))
		return
	}

	models, _, err := o.models.List(ctx)
	if err != nil && len(models) == 0 {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Failed to fetch models: %v", err))
		return
	}

	valid := false
	for _, m := range models {
		if m.ID == requested {
			valid = true
			break
		}
	}
	if !valid {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Unknown model %q. Use /models to list available models.", requested))
		return
	}

	if requested == currentModel {
		o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Model already set to %s.", requested))
		return
	}

	if err := o.updateGroupModel(ctx, chatJID, requested); err != nil {
		o.log.Error("failed to update model", "chat_jid", chatJID, "model", requested, "error", err)
		o.sendSystemMessage(ctx, chatJID, "Failed to update the model.")
		return
	}

	if err := o.sendModelUpdateToActive(ctx, chatJID, requested); err != nil {
		o.log.Error("failed to notify active sandbox of model update", "chat_jid", chatJID, "error", err)
	}

	o.sendSystemMessage(ctx, chatJID, fmt.Sprintf("Model set to %s.", requested))
}

func (o *Orchestrator) currentModelForChat(ctx context.Context, chatJID string) (string, string, error) {
	group, err := o.store.GetGroup(ctx, chatJID)
	if err != nil {
		return "", "", err
	}
	if group == nil {
		return "", "", fmt.Errorf("group not found")
	}
	if group.ContainerConfig == nil || group.ContainerConfig.Model == "" {
		return "", "default", nil
	}
	return group.ContainerConfig.Model, group.ContainerConfig.Model, nil
}

func (o *Orchestrator) updateGroupModel(ctx context.Context, chatJID string, model string) error {
	group, err := o.store.GetGroup(ctx, chatJID)
	if err != nil {
		return err
	}
	if group == nil {
		return fmt.Errorf("group not found")
	}
	if group.ContainerConfig == nil {
		group.ContainerConfig = &store.ContainerConfig{}
	}
	group.ContainerConfig.Model = model
	if err := o.store.UpsertGroup(ctx, group); err != nil {
		return err
	}
	if err := o.store.DeleteSession(ctx, group.Folder); err != nil {
		return err
	}

	o.mu.Lock()
	if existing, ok := o.registeredGroups[chatJID]; ok {
		existing.ContainerConfig = group.ContainerConfig
		o.registeredGroups[chatJID] = existing
	}
	delete(o.sessions, group.Folder)
	o.mu.Unlock()
	return nil
}

func (o *Orchestrator) sendModelUpdateToActive(ctx context.Context, chatJID string, model string) error {
	active, err := o.queue.IsActive(ctx, chatJID)
	if err != nil {
		return err
	}
	if !active {
		return nil
	}

	o.mu.Lock()
	group, ok := o.registeredGroups[chatJID]
	o.mu.Unlock()
	if !ok {
		return fmt.Errorf("group not found")
	}

	payload, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return err
	}

	return o.ipc.SendInput(ctx, group.Folder, &ipc.IPCMessage{
		Group:   group.Folder,
		Type:    ipc.IPCSetModel,
		Payload: payload,
	})
}

func (o *Orchestrator) sendSystemMessage(ctx context.Context, chatJID string, text string) {
	if err := o.router.RouteOutbound(ctx, chatJID, text); err != nil {
		o.log.Error("failed to send command response", "chat_jid", chatJID, "error", err)
	}
}
