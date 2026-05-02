package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type chatState int

const (
	chatStateSelectGroup chatState = iota
	chatStateSelectProvider
	chatStateSelectModel
	chatStateOAuth
	chatStateConnecting
	chatStateChatting
)

type groupRegisteredMsg struct {
	group *kraclawv1.Group
	err   error
}

type channelOutputMsg struct {
	msg *kraclawv1.InboundMessage
	err error
}

type inputSentMsg struct {
	err error
}

type streamOpenedMsg struct {
	stream kraclawv1.ChannelService_StreamInboundClient
	cancel context.CancelFunc
	err    error
}

func (m model) registerGroupCmd(name, provider, modelID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		jid := "tui:" + name
		req := &kraclawv1.RegisterGroupRequest{
			Jid:    jid,
			Name:   name,
			Folder: name,
			IsMain: true,
		}
		if provider != "" {
			cc := struct {
				Provider string `json:"provider"`
				Model    string `json:"model"`
			}{Provider: provider, Model: modelID}
			b, err := json.Marshal(cc)
			if err != nil {
				slog.Error("marshal container config", "err", err)
				return groupRegisteredMsg{err: fmt.Errorf("internal error preparing group configuration — please try again")}
			}
			req.ContainerConfigJson = string(b)
		}
		resp, err := m.api.groups.RegisterGroup(ctx, req)
		if err != nil {
			return groupRegisteredMsg{err: err}
		}
		return groupRegisteredMsg{group: resp}
	}
}

func (m model) openInboundStreamCmd(chatJID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := m.api.channels.StreamInbound(ctx, &kraclawv1.StreamInboundRequest{
			ChatJid: chatJID,
		})
		if err != nil {
			cancel()
			return streamOpenedMsg{err: err}
		}
		return streamOpenedMsg{stream: stream, cancel: cancel}
	}
}

func readInboundCmd(stream kraclawv1.ChannelService_StreamInboundClient) tea.Cmd {
	return func() tea.Msg {
		msg, err := stream.Recv()
		if err != nil {
			return channelOutputMsg{err: err}
		}
		return channelOutputMsg{msg: msg}
	}
}

func (m model) sendMessageCmd(text string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := m.api.channels.SendMessage(ctx, &kraclawv1.SendMessageRequest{
			ChatJid:    m.chatGroup.JID,
			Text:       text,
			Sender:     "tui-user",
			SenderName: "TUI User",
		})
		return inputSentMsg{err: err}
	}
}

func (m model) updateChat(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.chatState {
	case chatStateSelectGroup:
		if m.chatRegInput.Focused() {
			switch msg.String() {
			case "ctrl+c":
				return m, tea.Quit
			case "esc":
				m.chatRegInput.Blur()
				m.chatRegInput.SetValue("")
				return m, nil
			case "enter":
				name := strings.TrimSpace(m.chatRegInput.Value())
				if name == "" {
					return m, nil
				}
				m.chatRegInput.Blur()
				m.chatRegInput.SetValue("")
				m.creationPendingGroupName = name
				m.chatErr = nil
				m.chatState = chatStateSelectProvider
				m.creationPicker = creationPickerState{}
				m.creationProvidersLoaded = false
				m.creationFlowID++
				return m, m.fetchProvidersCmd(m.creationFlowID)
			default:
				var cmd tea.Cmd
				m.chatRegInput, cmd = m.chatRegInput.Update(msg)
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "q":
			return m, tea.Quit
		case "tab":
			m.activeTab = (m.activeTab + 1) % len(m.tabs)
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + len(m.tabs)) % len(m.tabs)
			return m, nil
		case "n":
			m.chatRegInput.Focus()
			return m, nil
		case "j", "down":
			if len(m.groups) > 0 && m.chatGroupCursor < len(m.groups)-1 {
				m.chatGroupCursor++
			}
			return m, nil
		case "k", "up":
			if m.chatGroupCursor > 0 {
				m.chatGroupCursor--
			}
			return m, nil
		case "enter":
			if len(m.groups) > 0 {
				g := m.groups[m.chatGroupCursor]
				m.chatGroup = &g
				m.chatState = chatStateConnecting
				m.chatErr = nil
				m.chatMessages = nil
				m.chatModel = ""
				return m, m.openInboundStreamCmd(g.JID)
			}
			return m, nil
		}

	case chatStateSelectProvider:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.chatState = chatStateSelectGroup
			m.chatErr = nil
			m.creationPendingGroupName = ""
			m.creationPicker = creationPickerState{}
			m.creationProvidersLoaded = false
			return m, nil
		case "j", "down":
			if m.creationPicker.cursor < len(m.creationPicker.items)-1 {
				m.creationPicker.cursor++
			}
			return m, nil
		case "k", "up":
			if m.creationPicker.cursor > 0 {
				m.creationPicker.cursor--
			}
			return m, nil
		case "enter":
			if !m.creationProvidersLoaded {
				return m, nil
			}
			if len(m.creationPicker.items) == 0 {
				return m, nil
			}
			selected := m.creationPicker.items[m.creationPicker.cursor]
			var modelItems []creationPickerItem
			for _, p := range m.creationProviders {
				if p.GetId() == selected.id {
					for _, mi := range p.GetModels() {
						modelItems = append(modelItems, creationPickerItem{
							id:    mi.GetId(),
							label: mi.GetDisplayName(),
						})
					}
					break
				}
			}
			if len(modelItems) == 0 {
				m.chatErr = fmt.Errorf("provider %q has no models configured — select a different provider or press Esc to cancel", selected.id)
				return m, nil
			}
			m.creationSelectedProvider = selected.id
			m.creationPicker = creationPickerState{items: modelItems}
			m.chatState = chatStateSelectModel
			return m, nil
		}

	case chatStateSelectModel:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.chatState = chatStateSelectProvider
			items, cursor := buildProviderItems(m.creationProviders, m.creationSelectedProvider)
			m.creationPicker = creationPickerState{items: items, cursor: cursor}
			return m, nil
		case "j", "down":
			if m.creationPicker.cursor < len(m.creationPicker.items)-1 {
				m.creationPicker.cursor++
			}
			return m, nil
		case "k", "up":
			if m.creationPicker.cursor > 0 {
				m.creationPicker.cursor--
			}
			return m, nil
		case "enter":
			if len(m.creationPicker.items) == 0 {
				return m, nil
			}
			selectedModel := m.creationPicker.items[m.creationPicker.cursor].id
			name := m.creationPendingGroupName
			provider := m.creationSelectedProvider
			authMode := lookupAuthMode(m.creationProviders, provider)

			m.creationSelectedModelID = selectedModel

			if authMode == "chatgpt" {
				groupJID := "tui:" + name
				m.oauth = oauthState{
					provider:         provider,
					groupJID:         groupJID,
					pendingGroupName: name,
				}
				m.chatState = chatStateOAuth
				return m, m.startOAuthCmd(provider, groupJID)
			}

			m.creationPendingGroupName = ""
			m.creationSelectedProvider = ""
			m.creationPicker = creationPickerState{}
			m.creationProviders = nil
			m.creationProvidersLoaded = false
			m.chatState = chatStateConnecting
			m.chatMessages = nil
			return m, m.registerGroupCmd(name, provider, selectedModel)
		}

	case chatStateOAuth:
		switch msg.String() {
		case "ctrl+c":
			if m.oauth.cancel != nil {
				m.oauth.cancel()
			}
			return m, tea.Quit
		case "esc":
			return m.handleEscOAuth()
		}

	case chatStateConnecting:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.chatState = chatStateSelectGroup
			return m, nil
		}

	case chatStateChatting:
		if m.modelPicker.Open {
			switch msg.String() {
			case "esc":
				m.modelPicker.Open = false
				m.modelPicker.Loading = false
				return m, nil
			case "j", "down":
				if m.modelPicker.Cursor < len(m.modelPicker.Options)-1 {
					m.modelPicker.Cursor++
				}
				return m, nil
			case "k", "up":
				if m.modelPicker.Cursor > 0 {
					m.modelPicker.Cursor--
				}
				return m, nil
			case "enter":
				if m.modelPicker.Cursor >= 0 && m.modelPicker.Cursor < len(m.modelPicker.Options) {
					selected := m.modelPicker.Options[m.modelPicker.Cursor]
					m.modelPicker.Open = false
					m.modelPicker.Loading = false
					return m, m.sendMessageCmd("/model " + selected.ID)
				}
				return m, nil
			}
		}

		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			m.chatState = chatStateSelectGroup
			m.chatWaitingForAgent = false
			m.modelPicker.Open = false
			m.modelPicker.Loading = false
			m.chatInput.Blur()
			if m.chatCancel != nil {
				m.chatCancel()
				m.chatCancel = nil
			}
			m.inboundStream = nil
			return m, nil
		case "enter":
			input := strings.TrimSpace(m.chatInput.Value())
			// :auth <provider> — re-authenticate the current group's OAuth
			// credentials in place. Useful when refresh tokens are revoked or
			// expire mid-session. Bare ":auth" surfaces a usage error.
			if isAuthCommand(input) {
				parts := strings.Fields(input)
				if len(parts) < 2 {
					m.chatErr = fmt.Errorf("usage: :auth <provider>")
					return m, nil
				}
				provider := parts[1]
				m.chatInput.Reset()
				m.chatInput.SetValue("")
				m.oauth = oauthState{
					provider: provider,
					groupJID: m.chatGroup.JID,
				}
				m.chatState = chatStateOAuth
				m.chatErr = nil
				return m, m.startOAuthCmd(provider, m.chatGroup.JID)
			}
			if input == "" {
				return m, nil
			}
			if cmd, handled := m.handleLocalCommand(input); handled {
				m.chatInput.Reset()
				m.chatInput.SetValue("")
				m = m.refreshChatViewportContent()
				return m, cmd
			}
			m.chatMessages = append(m.chatMessages, chatMessage{
				sender:  "you",
				content: input,
			})
			m.chatInput.Reset()
			m.chatInput.SetValue("")
			contentWidth := m.chatViewport.Width() - 6
			if contentWidth < 10 {
				contentWidth = 10
			}
			m.ensureMarkdownRenderer(contentWidth)
			m.chatViewport.SetContent(m.formatChatMessages())
			m.chatViewport.GotoBottom()
			m.chatWaitingForAgent = true
			return m, m.sendMessageCmd(input)
		case "ctrl+m":
			m.modelPicker.Open = true
			m.modelPicker.Loading = true
			m.modelPicker.Options = nil
			m.modelPicker.Cursor = 0
			m.modelPicker.LastError = ""
			return m, m.sendMessageCmd("/models")
		case "ctrl+u":
			m.chatViewport.HalfPageUp()
			return m, nil
		case "ctrl+d":
			m.chatViewport.HalfPageDown()
			return m, nil
		case "pgup":
			m.chatViewport.HalfPageUp()
			return m, nil
		case "pgdown":
			m.chatViewport.HalfPageDown()
			return m, nil
		default:
			var cmd tea.Cmd
			m.chatInput, cmd = m.chatInput.Update(msg)
			return m, cmd
		}
	}

	return m, nil
}

// handleLocalCommand intercepts composer text that begins with ":" so users
// can change the theme without sending traffic to the agent. Returns true
// when the input was consumed locally.
func (m *model) handleLocalCommand(text string) (tea.Cmd, bool) {
	if !strings.HasPrefix(text, ":") {
		return nil, false
	}
	parts := strings.Fields(text)
	switch parts[0] {
	case ":theme":
		next := "dark"
		if len(parts) > 1 {
			next = strings.ToLower(parts[1])
		} else if activePalette.Name == "dark" {
			next = "light"
		}
		if next != "light" && next != "dark" {
			next = "dark"
		}
		setTheme(next)
		_ = persistTheme(next)
		return nil, true
	}
	return nil, false
}

func (m model) renderChat() string {
	var b strings.Builder
	w := m.contentWidth()

	switch m.chatState {
	case chatStateSelectGroup:
		b.WriteString(" " + coralStyle.Render("messages") + " " + dimStyle.Render("· select a group") + "\n\n")

		if m.chatErr != nil {
			b.WriteString("  " + errStyle.Render("error: "+m.chatErr.Error()) + "\n\n")
		}

		if m.chatRegInput.Focused() {
			b.WriteString(sectionRule(w, "new group") + "\n")
			b.WriteString("  " + composerPromptStyle.Render("▌ ") + m.chatRegInput.View() + "\n\n")
			b.WriteString("  " + dimStyle.Render("⏎ create · esc cancel") + "\n")
			return b.String()
		}

		b.WriteString(sectionRule(w, "groups") + "\n")
		if len(m.groups) == 0 {
			b.WriteString("  " + dimStyle.Render("no groups yet. press ") + coralStyle.Render("n") + dimStyle.Render(" to create one.") + "\n")
			return b.String()
		}
		for i, g := range m.groups {
			name := g.Name
			if name == "" {
				name = g.Folder
			}
			line := fmt.Sprintf("  %-24s %s", truncateWidth(name, 24), dimStyle.Render(g.JID))
			if i == m.chatGroupCursor {
				b.WriteString(selStrongStyle.Render(padRight(fmt.Sprintf("  %-24s %s", truncateWidth(name, 24), g.JID), w)))
			} else {
				b.WriteString(fgStyle.Render(line))
			}
			b.WriteString("\n")
		}

	case chatStateSelectProvider:
		b.WriteString(" " + coralStyle.Render("messages") + " " + dimStyle.Render("· select provider") + "\n\n")
		b.WriteString("  " + dimStyle.Render("new group ") + fgStyle.Render(fmt.Sprintf("%q", m.creationPendingGroupName)) + "\n\n")

		if m.chatErr != nil {
			b.WriteString("  " + errStyle.Render("error: "+m.chatErr.Error()) + "\n\n")
		}

		b.WriteString(sectionRule(w, "providers") + "\n")
		switch {
		case !m.creationProvidersLoaded:
			b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("loading providers...") + "\n")
		case len(m.creationPicker.items) == 0:
			b.WriteString("  " + errStyle.Render("No providers are configured on this server.") + "\n")
			b.WriteString("  " + dimStyle.Render("press esc to go back.") + "\n")
		default:
			for i, item := range m.creationPicker.items {
				renderPickerRow(&b, item.label, i == m.creationPicker.cursor, w)
			}
		}

	case chatStateSelectModel:
		b.WriteString(" " + coralStyle.Render("messages") + " " + dimStyle.Render("· select model") + "\n\n")
		b.WriteString("  " + dimStyle.Render("provider ") + fgStyle.Render(m.creationSelectedProvider) + "\n\n")

		if m.chatErr != nil {
			b.WriteString("  " + errStyle.Render("error: "+m.chatErr.Error()) + "\n\n")
		}

		b.WriteString(sectionRule(w, "models") + "\n")
		for i, item := range m.creationPicker.items {
			renderPickerRow(&b, item.label, i == m.creationPicker.cursor, w)
		}

	case chatStateOAuth:
		b.WriteString(sectionRule(w, "chatgpt oauth") + "\n")
		b.WriteString(renderOAuth(m.oauth))
		b.WriteString("\n")
		b.WriteString("  " + dimStyle.Render("Esc: cancel"))

	case chatStateConnecting:
		b.WriteString(" " + coralStyle.Render("messages") + " " + dimStyle.Render("· connecting") + "\n\n")
		name := ""
		if m.chatGroup != nil {
			name = m.chatGroup.Name
			if name == "" {
				name = m.chatGroup.Folder
			}
		}
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("connecting to ") + fgStyle.Render(name) + dimStyle.Render("...") + "\n")

	case chatStateChatting:
		name := m.chatGroup.Name
		if name == "" {
			name = m.chatGroup.Folder
		}
		channel := "tui"
		provider := coalesce(m.chatModel, "default")
		var sbxID, sbxState string
		for _, sb := range m.sandboxes {
			if sb.GroupJID == m.chatGroup.JID {
				sbxID = sb.Name
				sbxState = sb.State
				break
			}
		}

		// Group header: `groups › <name> · <channel> · <provider> · sbx-… · <state>`
		parts := []string{
			groupHeaderStyle.Render("groups ›"),
			fgStyle.Render(name),
			dimStyle.Render("· " + channel),
			dimStyle.Render("· " + provider),
		}
		if sbxID != "" {
			parts = append(parts, dimStyle.Render("· ")+coralStyle.Render(sbxID))
		}
		if sbxState != "" {
			parts = append(parts, dimStyle.Render("· ")+stateStyle(sbxState).Render(sbxState))
		}
		if m.chatWaitingForAgent {
			parts = append(parts, dimStyle.Render("· ")+coralStyle.Render("typing "+m.spinner.View()))
		}
		b.WriteString(strings.Join(parts, " ") + "\n")

		if m.chatErr != nil {
			b.WriteString("  " + errStyle.Render("error: "+m.chatErr.Error()) + "\n")
		}

		b.WriteString(m.chatViewport.View())

		if m.modelPicker.Open {
			b.WriteString("\n")
			b.WriteString(m.renderModelPicker(w))
		}

		b.WriteString("\n")
		b.WriteString(renderInputBox(m.chatInput.View(), m.width))
		b.WriteString("\n")
		b.WriteString(m.renderComposerMeta(name))
	}

	return b.String()
}

func (m model) renderComposerMeta(groupName string) string {
	limit := m.chatInput.CharLimit
	if limit <= 0 {
		limit = 4000
	}
	used := len(m.chatInput.Value())
	meta := fmt.Sprintf("%s · markdown · %d/%d", groupName, used, limit)
	return "  " + composerMetaStyle.Render(meta)
}

func (m model) renderModelPicker(width int) string {
	var b strings.Builder
	b.WriteString(sectionRule(width, "model") + "\n")
	if m.modelPicker.Loading {
		b.WriteString("  " + m.spinner.View() + " " + dimStyle.Render("loading models...") + "\n")
	}
	if m.modelPicker.LastError != "" {
		b.WriteString("  " + errStyle.Render(m.modelPicker.LastError) + "\n")
	}
	if !m.modelPicker.Loading && m.modelPicker.LastError == "" && len(m.modelPicker.Options) == 0 {
		b.WriteString("  " + errStyle.Render("No models available") + "\n")
		b.WriteString("  " + dimStyle.Render("Check your agent configuration or retry `/models` to load available models.") + "\n")
	}
	for i, option := range m.modelPicker.Options {
		label := option.Label
		if option.Current {
			label += " (current)"
		}
		renderPickerRow(&b, label, i == m.modelPicker.Cursor, width)
	}
	b.WriteString("  " + dimStyle.Render("⏎ apply · esc close") + "\n")
	return b.String()
}

func renderPickerRow(b *strings.Builder, label string, selected bool, width int) {
	raw := "  " + label
	if selected {
		b.WriteString(selStrongStyle.Render(padRight(raw, width)))
	} else {
		b.WriteString(fgStyle.Render(raw))
	}
	b.WriteString("\n")
}

func coalesce(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func isAuthCommand(input string) bool {
	parts := strings.Fields(input)
	return len(parts) > 0 && parts[0] == ":auth"
}

func lookupAuthMode(providers []*kraclawv1.ProviderInfo, id string) string {
	for _, p := range providers {
		if p.GetId() == id {
			return p.GetAuthMode()
		}
	}
	return ""
}
