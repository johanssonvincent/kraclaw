package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	kraclawv1 "github.com/johanssonvincent/kraclaw/pkg/pb/kraclawv1"
)

type chatState int

const (
	chatStateSelectGroup    chatState = iota
	chatStateSelectProvider          // step 1: pick provider for new group
	chatStateSelectModel             // step 2: pick model for new group
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

func (m model) registerGroupCmd(name, provider, model string) tea.Cmd {
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
			}{Provider: provider, Model: model}
			if b, err := json.Marshal(cc); err == nil {
				req.ContainerConfigJson = string(b)
			}
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
		// Check if reg input is focused (user is typing a new group name)
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
				return m, m.fetchProvidersCmd()
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
			m.creationPendingGroupName = ""
			m.creationPicker = creationPickerState{}
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
			selected := m.creationPicker.items[m.creationPicker.cursor]
			m.creationSelectedProvider = selected.id
			// Load model list for the selected provider from cached providers.
			m.creationPicker = creationPickerState{}
			for _, p := range m.creationProviders {
				if p.GetId() == selected.id {
					for _, mi := range p.GetModels() {
						m.creationPicker.items = append(m.creationPicker.items, creationPickerItem{
							id:    mi.GetId(),
							label: mi.GetDisplayName(),
						})
					}
					break
				}
			}
			m.chatState = chatStateSelectModel
			return m, nil
		}

	case chatStateSelectModel:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			// Back to provider picker.
			m.chatState = chatStateSelectProvider
			m.creationPicker = creationPickerState{}
			for _, p := range m.creationProviders {
				m.creationPicker.items = append(m.creationPicker.items, creationPickerItem{
					id:    p.GetId(),
					label: p.GetDisplayName(),
				})
			}
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
			m.creationPendingGroupName = ""
			m.creationSelectedProvider = ""
			m.creationPicker = creationPickerState{}
			m.creationProviders = nil
			m.chatState = chatStateConnecting
			m.chatMessages = nil
			return m, m.registerGroupCmd(name, provider, selectedModel)
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
			text := strings.TrimSpace(m.chatInput.Value())
			if text == "" {
				return m, nil
			}
			m.chatMessages = append(m.chatMessages, chatMessage{
				sender:  "you",
				content: text,
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
			return m, m.sendMessageCmd(text)
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

func (m model) renderChat() string {
	var b strings.Builder

	switch m.chatState {
	case chatStateSelectGroup:
		b.WriteString(titleStyle.Render("Chat - Select Group"))
		b.WriteString("\n")

		if m.chatErr != nil {
			b.WriteString(errStyle.Render("  Error: "+m.chatErr.Error()) + "\n\n")
		}

		if m.chatRegInput.Focused() {
			b.WriteString("  New group name: ")
			b.WriteString(m.chatRegInput.View())
			b.WriteString("\n")
			b.WriteString(dimStyle.Render("  Enter: create | Esc: cancel"))
			return b.String()
		}

		if len(m.groups) == 0 {
			b.WriteString(dimStyle.Render("  No groups available. Press 'n' to create one."))
			return b.String()
		}

		for i, g := range m.groups {
			cursor := "  "
			if i == m.chatGroupCursor {
				cursor = "> "
			}
			name := g.Name
			if name == "" {
				name = g.Folder
			}
			line := fmt.Sprintf("%s%-20s %s", cursor, name, dimStyle.Render(g.JID))
			if i == m.chatGroupCursor {
				b.WriteString(okStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  n: new group | Enter: select | j/k: navigate"))

	case chatStateSelectProvider:
		b.WriteString(titleStyle.Render("Chat - Select Provider"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("  New group: %q\n", m.creationPendingGroupName)))
		if len(m.creationPicker.items) == 0 {
			b.WriteString("  " + m.spinner.View() + " Loading providers...\n")
		} else {
			for i, item := range m.creationPicker.items {
				cursor := "  "
				if i == m.creationPicker.cursor {
					cursor = "> "
				}
				line := cursor + item.label
				if i == m.creationPicker.cursor {
					b.WriteString(okStyle.Render(line))
				} else {
					b.WriteString(line)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Enter: select | Esc: back | j/k: navigate"))

	case chatStateSelectModel:
		b.WriteString(titleStyle.Render("Chat - Select Model"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("  Provider: %s\n", m.creationSelectedProvider)))
		for i, item := range m.creationPicker.items {
			cursor := "  "
			if i == m.creationPicker.cursor {
				cursor = "> "
			}
			line := cursor + item.label
			if i == m.creationPicker.cursor {
				b.WriteString(okStyle.Render(line))
			} else {
				b.WriteString(line)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  Enter: create group | Esc: back | j/k: navigate"))

	case chatStateConnecting:
		b.WriteString(titleStyle.Render("Chat"))
		b.WriteString("\n")
		name := ""
		if m.chatGroup != nil {
			name = m.chatGroup.Name
			if name == "" {
				name = m.chatGroup.Folder
			}
		}
		b.WriteString("  " + m.spinner.View() + " Connecting to " + name + "...")

	case chatStateChatting:
		name := m.chatGroup.Name
		if name == "" {
			name = m.chatGroup.Folder
		}
		title := "Chat - " + name
		if m.chatWaitingForAgent {
			title += "  " + m.spinner.View() + " Waiting..."
		}
		b.WriteString(titleStyle.Render(title))
		b.WriteString("\n")

		if m.chatErr != nil {
			b.WriteString(errStyle.Render("  Error: "+m.chatErr.Error()) + "\n")
		}

		// Populate sidebar with current state (must happen before View() call)
		m.sidebar.messageCount = len(m.chatMessages)
		if m.status != nil {
			m.sidebar.uptime = m.status.UptimeSince
		}
		if m.chatGroup != nil {
			for _, sb := range m.sandboxes {
				if sb.GroupJID == m.chatGroup.JID && sb.SessionID != "" {
					m.sidebar.sessionID = sb.SessionID
					break
				}
			}
		}
		m.sidebar.model = coalesce(m.chatModel, "default")
		m.sidebar.processing = "Idle"
		if m.chatWaitingForAgent {
			m.sidebar.processing = "Waiting"
		}

		// Compose horizontal layout: viewport | gap | sidebar (LAYOUT-01)
		vpView := m.chatViewport.View()
		if m.sidebarVisible {
			sidebarView := m.sidebar.View()
			b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, vpView, " ", sidebarView))
		} else {
			b.WriteString(vpView)
		}

		if m.modelPicker.Open {
			b.WriteString("\n")
			var picker strings.Builder
			picker.WriteString("Model selection\n")
			if m.modelPicker.Loading {
				picker.WriteString("Loading models...\n")
			}
			if m.modelPicker.LastError != "" {
				picker.WriteString(m.modelPicker.LastError + "\n")
			}
			if !m.modelPicker.Loading && m.modelPicker.LastError == "" && len(m.modelPicker.Options) == 0 {
				picker.WriteString("No models available\n")
				picker.WriteString("Check your agent configuration or retry `/models` to load available models.\n")
			}
			for i, option := range m.modelPicker.Options {
				cursor := "  "
				if i == m.modelPicker.Cursor {
					cursor = "> "
				}
				line := option.Label
				if option.Current {
					line += " (current)"
				}
				picker.WriteString(cursor + line + "\n")
			}
			picker.WriteString("Enter: apply | Esc: close")
			b.WriteString(lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderForeground(defaultChatTheme.PanelBorder).
				PaddingLeft(1).
				PaddingRight(1).
				Render(strings.TrimRight(picker.String(), "\n")))
		}

		b.WriteString("\n")
		b.WriteString(renderInputBox(m.chatInput.View(), m.width))
	}

	return b.String()
}
