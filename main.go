package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/wish/activeterm"

	// markdown rendering
	"github.com/charmbracelet/glamour"

	// Chat
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	// SSH
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/bubbletea"
	"github.com/charmbracelet/wish/logging"
)

const (
	host = "0.0.0.0"
	port = "8080"
	gap  = "\n\n"
)

var (
	// Global message channel that all clients will send to and listen on
	msgChan = make(chan chatMessage)

	// Map to keep track of active client channels
	clients = struct {
		sync.RWMutex
		channels map[string]chan chatMessage
	}{channels: make(map[string]chan chatMessage)}
)

// chatMessage represents a message in the chat
type chatMessage struct {
	sender  string
	content string
	time    time.Time
}

type (
	errMsg error
)

// contains all state for a particular session
type model struct {
	username    string
	clientID    string
	clientChan  chan chatMessage
	mdRenderer  *glamour.TermRenderer // markdown renderer
	viewport    viewport.Model
	messages    []string
	textarea    textarea.Model
	senderStyle lipgloss.Style
	err         error
}

// starts the global message listener that broadcasts messages to all clients
func startMessageBroadcaster() {
	go func() {
		for msg := range msgChan {
			// Broadcast message to all clients
			clients.RLock()
			for _, ch := range clients.channels {
				// Non-blocking send to each client
				select {
				case ch <- msg:
					// Message sent successfully
				default:
					// Channel is full or closed, skip this one
				}
			}
			clients.RUnlock()
		}
	}()
}

// Register a new client to receive messages
func registerClient(id string) chan chatMessage {
	clientChan := make(chan chatMessage, 100) // Buffer for messages

	clients.Lock()
	clients.channels[id] = clientChan
	clients.Unlock()

	return clientChan
}

// Unregister a client when they disconnect
func unregisterClient(id string) {
	clients.Lock()
	defer clients.Unlock()

	if ch, ok := clients.channels[id]; ok {
		close(ch)
		delete(clients.channels, id)
	}
}

// tea.Cmd that checks for new messages
func checkMessagesCmd(ch chan chatMessage) tea.Cmd {
	return func() tea.Msg {
		ticker := time.NewTicker(100 * time.Millisecond)
		<-ticker.C
		return checkNewMessages(ch)
	}
}

func checkNewMessages(ch chan chatMessage) tea.Msg {
	select {
	case msg, ok := <-ch:
		if !ok {
			return nil // Channel closed
		}
		return msg
	default:
		// No message available, don't block
		return nil
	}
}

// func initialModel() model {

// 	ta := textarea.New()
// 	ta.Placeholder = "Send a message..."
// 	ta.Focus()

// 	ta.Prompt = "┃ "
// 	ta.CharLimit = 1000

// 	ta.SetWidth(30)
// 	ta.SetHeight(3)

// 	// Remove cursor line styling
// 	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()

// 	ta.ShowLineNumbers = false

// 	vp := viewport.New(30, 5)
// 	vp.SetContent(`Welcome to the chat room!
// Type a message and press Enter to send.`)

// 	ta.KeyMap.InsertNewline.SetEnabled(false)

// 	return model{
// 		textarea:    ta,
// 		messages:    []string{},
// 		viewport:    vp,
// 		senderStyle: lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
// 		err:         nil,
// 	}
// }

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		checkMessagesCmd(m.clientChan),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {

	var (
		tiCmd tea.Cmd
		vpCmd tea.Cmd
	)

	m.textarea, tiCmd = m.textarea.Update(msg)
	m.viewport, vpCmd = m.viewport.Update(msg)

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport.Width = msg.Width
		m.textarea.SetWidth(msg.Width)
		m.viewport.Height = msg.Height - m.textarea.Height() - lipgloss.Height(gap)

		if len(m.messages) > 0 {
			// Wrap content before setting it.
			m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n")))
		}
		m.viewport.GotoBottom()
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Regular Enter - send the message
			if m.textarea.Value() != "" {
				// Create and broadcast the message
				newMsg := chatMessage{
					sender:  m.username,
					content: m.textarea.Value(),
					time:    time.Now(),
				}
				msgChan <- newMsg

				// Clear the input
				m.textarea.Reset()
				m.viewport.GotoBottom()
			}
			// Skip default textarea handling for this key
			return m, checkMessagesCmd(m.clientChan)
		case "shift+enter":
			// Shift+Enter - insert a newline
			m.textarea, tiCmd = m.textarea.Update(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune("\n"),
			})
			return m, tea.Batch(tiCmd, checkMessagesCmd(m.clientChan))
		case "escape", "ctrl+c":
			// Send leave message before quitting
			leaveMsg := chatMessage{
				sender:  "system",
				content: fmt.Sprintf("%s has left the chat", m.username),
				time:    time.Now(),
			}
			msgChan <- leaveMsg

			// Unregister the client
			unregisterClient(m.clientID)
			return m, tea.Quit
		}
		// switch msg.Type {
		// case tea.KeyCtrlC, tea.KeyEsc:
		// 	// Send leave message before quitting
		// 	leaveMsg := chatMessage{
		// 		sender:  "system",
		// 		content: fmt.Sprintf("%s has left the chat", m.username),
		// 		time:    time.Now(),
		// 	}
		// 	msgChan <- leaveMsg

		// 	// Unregister the client
		// 	unregisterClient(m.clientID)
		// 	return m, tea.Quit
		// case tea.KeyEnter:
		// 	if m.textarea.Value() == "" {
		// 		return m, nil
		// 	}
		// 	// Create and broadcast the message
		// 	newMsg := chatMessage{
		// 		sender:  m.username,
		// 		content: m.textarea.Value(),
		// 		time:    time.Now(),
		// 	}
		// 	msgChan <- newMsg
		// m.messages = append(m.messages, m.senderStyle.Render(m.username+": ")+m.textarea.Value())
		// m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n")))

		//clear input
		// m.textarea.Reset()
		// m.viewport.GotoBottom()
		// }
	case chatMessage:
		// Format and add the received message
		// f, err := tea.LogToFile("debug.log", "debug")
		// if err != nil {
		// 	fmt.Println("fatal:", err)
		// 	os.Exit(1)
		// }
		// defer f.Close()

		var formattedMsg string
		if msg.sender == "system" {
			systemStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
			formattedMsg = systemStyle.Render(msg.content)
		} else {
			senderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
			senderPrefix := senderStyle.Render(msg.sender + ": ")
			// f.WriteString("Sender prefix: " + senderPrefix + "\n")
			// f.WriteString("Plain Text: " + msg.content + "\n")

			// str := strings.TrimSpace(msg.content)

			// Always try to render as Markdown
			rendered, err := m.mdRenderer.Render(msg.content)
			var content string
			if err == nil {
				content = strings.Trim(rendered, " \n\t")
				content = strings.TrimSpace(content)
			} else {
				// Fallback to plain text if rendering fails
				content = msg.content
			}

			// f.WriteString("Rendered content: " + content + "\n")
			// f.WriteString("Trimmed content: " + strings.TrimSpace(content) + "\n")

			formattedMsg = senderPrefix + content
			// f.WriteString(formattedMsg)

		}

		// //render markdown
		// out, _ := glamour.Render(formattedMsg, "dark")

		m.messages = append(m.messages, formattedMsg)
		m.viewport.SetContent(lipgloss.NewStyle().Width(m.viewport.Width).Render(strings.Join(m.messages, "\n")))
		m.viewport.GotoBottom()

		// Continue checking for messages
		return m, checkMessagesCmd(m.clientChan)

	// We handle errors just like any other message
	case errMsg:
		m.err = msg
		return m, nil
	}

	return m, tea.Batch(
		tiCmd,
		vpCmd,
		checkMessagesCmd(m.clientChan),
	)
	// return m, tea.Batch(tiCmd, vpCmd)
}

func (m model) View() string {
	// The header
	s := "Le Chat 0.1\n\n"

	// for _, user := range m.users {
	// 	s += fmt.Sprintf("[%s]: ", user)
	// }

	// for _, message := range m.messages {

	// 	// Render the row
	// 	s += fmt.Sprintf("%s\n", message)
	// }

	// // The footer
	// s += "\nPress q to quit.\n"

	// Send the UI for rendering
	s += fmt.Sprintf(
		"%s%s%s",
		m.viewport.View(),
		gap,
		m.textarea.View(),
	)

	return s
}

// You can wire any Bubble Tea model up to the middleware with a function that
// handles the incoming ssh.Session. Here we just grab the terminal info and
// pass it to the new model. You can also return tea.ProgramOptions (such as
// tea.WithAltScreen) on a session by session basis.
func teaHandler(s ssh.Session) (tea.Model, []tea.ProgramOption) {

	// When running a Bubble Tea app over SSH, you shouldn't use the default
	// lipgloss.NewStyle function.
	// That function will use the color profile from the os.Stdin, which is the
	// server, not the client.
	// We provide a MakeRenderer function in the bubbletea middleware package,
	// so you can easily get the correct renderer for the current session, and
	// use it to create the styles.
	// The recommended way to use these styles is to then pass them down to
	// your Bubble Tea model.
	// renderer := bubbletea.MakeRenderer(s)
	// txtStyle := renderer.NewStyle().Foreground(lipgloss.Color("10"))
	// quitStyle := renderer.NewStyle().Foreground(lipgloss.Color("8"))

	//QUESTION: How do we write this function?

	// Get a renderer for the current SSH session
	renderer := bubbletea.MakeRenderer(s)
	mdRenderer, _ := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(80), // Adjust based on your needs
	)

	// Initialize the textarea
	ta := textarea.New()
	ta.Placeholder = "Send a message..."
	ta.Focus()
	ta.Prompt = "┃ "
	ta.CharLimit = 1000
	ta.SetWidth(30)
	ta.SetHeight(5)
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.ShowLineNumbers = false
	// ta.KeyMap.InsertNewline.SetEnabled(false)

	// Initialize the viewport
	vp := viewport.New(30, 5)
	vp.SetContent(`Welcome to the chat room!
Type a message and press Enter to send.`)

	// Get the SSH username to display in the chat
	username := s.User()
	if username == "" {
		username = "Anonymous"
	}

	// Generate a unique client ID
	clientID := fmt.Sprintf("%s-%d", username, time.Now().UnixNano())

	// Register this client to receive messages
	clientChan := registerClient(clientID)

	// Create a command to check for new messages
	// checkMessages := func() tea.Msg {
	// 	return checkNewMessages(clientChan)
	// }

	m := model{
		username:    username,
		clientID:    clientID,
		clientChan:  clientChan,
		mdRenderer:  mdRenderer,
		textarea:    ta,
		messages:    []string{},
		viewport:    vp,
		senderStyle: renderer.NewStyle().Foreground(lipgloss.Color("5")),
		err:         nil,
	}

	// Add system message about user joining
	joinMsg := chatMessage{
		sender:  "system",
		content: fmt.Sprintf("%s has joined the chat", username),
		time:    time.Now(),
	}
	msgChan <- joinMsg

	return m, []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithContext(context.Background()),
	}
}

func main() {

	startMessageBroadcaster()

	s, err := wish.NewServer(
		wish.WithAddress(net.JoinHostPort(host, port)),
		wish.WithHostKeyPath(".ssh/id_ed25519"),
		wish.WithMiddleware(
			bubbletea.Middleware(teaHandler),
			logging.Middleware(),
			activeterm.Middleware(),
		),
	)
	if err != nil {
		log.Error("Could not start server", "error", err)
	}
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	log.Info("Starting SSH server", "host", host, "port", port)
	go func() {
		if err = s.ListenAndServe(); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			log.Error("Could not start server", "error", err)
			done <- nil
		}
	}()

	<-done
	log.Info("Stopping SSH server")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer func() { cancel() }()
	if err := s.Shutdown(ctx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
		log.Error("Could not stop server", "error", err)
	}

}
