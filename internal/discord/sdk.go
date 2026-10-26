package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// DiscordClient owns its network operations and never exposes its token in errors.
type DiscordClient struct {
	token, apiBase  string
	http            *http.Client
	gate            chan struct{}
	reset           time.Time
	mu              sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	OnMessage       func(*discordgo.MessageCreate)
	OnMessageUpdate func(*discordgo.MessageUpdate)
	OnMessageDelete func(*discordgo.MessageDelete)
	OnError         func(error)
	OnInteraction   func(*discordgo.InteractionCreate)
	OnReady         func(*discordgo.Ready)
}

func NewDiscordClient(token string) (*DiscordClient, error) {
	if token == "" {
		return nil, errors.New("discord: empty bot token")
	}
	return &DiscordClient{token: token, apiBase: "https://discord.com/api/v10", http: &http.Client{Timeout: 20 * time.Second}, gate: make(chan struct{}, 1)}, nil
}

// REST makes an authenticated, context-bound Discord API request. Paths are relative to the API root.
func (c *DiscordClient) REST(ctx context.Context, method, path string, payload, out any) error {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\r\n") {
		return errors.New("discord: invalid API path")
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return errors.New("discord: invalid request")
		}
	}
	select {
	case c.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.gate }()
	for tries := 0; tries < 8; tries++ {
		if wait := time.Until(c.reset); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, bytes.NewReader(body))
		if err != nil {
			return errors.New("discord: invalid request")
		}
		req.Header.Set("Authorization", "Bot "+c.token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("discord: request failed")
		}
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			if seconds, e := strconv.ParseFloat(resp.Header.Get("X-RateLimit-Reset-After"), 64); e == nil && seconds > 0 && seconds <= 60 {
				c.reset = time.Now().Add(time.Duration(seconds * float64(time.Second)))
			}
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return errors.New("discord: response failed")
		}
		if resp.StatusCode == 429 {
			var rate struct {
				RetryAfter float64 `json:"retry_after"`
			}
			if json.Unmarshal(data, &rate) != nil || rate.RetryAfter <= 0 || rate.RetryAfter > 60 {
				return errors.New("discord: invalid rate limit")
			}
			timer := time.NewTimer(time.Duration(rate.RetryAfter * float64(time.Second)))
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("discord: API status %d", resp.StatusCode)
		}
		if out != nil {
			if len(data) == 0 || string(bytes.TrimSpace(data)) == "null" || json.Unmarshal(data, out) != nil {
				return errors.New("discord: invalid response")
			}
		}
		return nil
	}
	return errors.New("discord: rate limit retries exhausted")
}
func (c *DiscordClient) Open(ctx context.Context) error {
	c.mu.Lock()
	if c.cancel != nil {
		c.mu.Unlock()
		return errors.New("discord: gateway already running")
	}
	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})
	done := c.done
	c.mu.Unlock()
	ready := make(chan error, 1)
	go c.gateway(runCtx, ready, done)
	select {
	case err := <-ready:
		if err != nil {
			_ = c.Close()
		}
		return err
	case <-ctx.Done():
		_ = c.Close()
		return ctx.Err()
	}
}
func (c *DiscordClient) Close() error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	<-done
	c.mu.Lock()
	if c.done == done {
		c.cancel = nil
		c.done = nil
	}
	c.mu.Unlock()
	return nil
}

type gatewayFrame struct {
	Op       int             `json:"op"`
	Data     json.RawMessage `json:"d"`
	Sequence *int64          `json:"s"`
	Type     string          `json:"t"`
}

func (c *DiscordClient) gateway(ctx context.Context, ready chan<- error, done chan<- struct{}) {
	defer close(done)
	connected := false
	defer func() {
		if !connected {
			select {
			case ready <- errors.New("discord: gateway stopped"):
			default:
			}
		}
	}()
	var gateway struct {
		URL string `json:"url"`
	}
	if err := c.REST(ctx, "GET", "/gateway", nil, &gateway); err != nil {
		ready <- err
		return
	}
	sessionID := ""
	resumeURL := ""
	var seq int64
	backoff := time.Second
	for ctx.Err() == nil {
		base := gateway.URL
		if sessionID != "" && resumeURL != "" {
			base = resumeURL
		}
		err := c.gatewayConnection(ctx, base, &sessionID, &resumeURL, &seq, func() {
			if !connected {
				connected = true
				ready <- nil
			}
			backoff = time.Second
		})
		status := websocket.CloseStatus(err)
		if status == 4007 || status == 4009 {
			sessionID = ""
			resumeURL = ""
			seq = 0
		}
		fatal := status == 4004 || status == 4010 || status == 4011 || status == 4012 || status == 4013 || status == 4014
		if fatal {
			e := fmt.Errorf("discord: gateway authentication or configuration failure (%d)", status)
			if !connected {
				ready <- e
			} else if c.OnError != nil {
				c.OnError(e)
			}
			return
		}
		if !connected {
			if ctx.Err() != nil {
				ready <- ctx.Err()
			} else {
				ready <- errors.New("discord: gateway connection failed")
			}
			return
		}
		if ctx.Err() != nil {
			return
		}
		_ = err
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
func (c *DiscordClient) gatewayConnection(ctx context.Context, base string, sessionID, resumeURL *string, seq *int64, onReady func()) error {
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") {
		return errors.New("discord: invalid gateway")
	}
	q := u.Query()
	q.Set("v", "10")
	q.Set("encoding", "json")
	u.RawQuery = q.Encode()
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	conn, _, err := websocket.Dial(dialCtx, u.String(), nil)
	cancel()
	if err != nil {
		return err
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(1 << 20)
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	var hello gatewayFrame
	err = wsjson.Read(readCtx, conn, &hello)
	cancel()
	if err != nil || hello.Op != 10 {
		return errors.New("discord: invalid gateway hello")
	}
	var h struct {
		HeartbeatInterval int `json:"heartbeat_interval"`
	}
	if json.Unmarshal(hello.Data, &h) != nil || h.HeartbeatInterval <= 0 {
		return errors.New("discord: invalid heartbeat")
	}
	var current atomic.Int64
	current.Store(*seq)
	var sequenceKnown atomic.Bool
	sequenceKnown.Store(*sessionID != "")
	heartbeatData := func() any {
		if !sequenceKnown.Load() {
			return nil
		}
		return current.Load()
	}
	var ack atomic.Bool
	ack.Store(true)
	connCtx, stop := context.WithCancel(ctx)
	defer stop()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(time.Duration(h.HeartbeatInterval) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-connCtx.Done():
				return
			case <-ticker.C:
				if !ack.Swap(false) {
					_ = conn.CloseNow()
					return
				}
				if wsjson.Write(connCtx, conn, map[string]any{"op": 1, "d": heartbeatData()}) != nil {
					_ = conn.CloseNow()
					return
				}
			}
		}
	}()
	defer func() { stop(); <-heartbeatDone }()
	var identify any
	if *sessionID != "" {
		identify = map[string]any{"op": 6, "d": map[string]any{"token": "Bot " + c.token, "session_id": *sessionID, "seq": *seq}}
	} else {
		identify = map[string]any{"op": 2, "d": map[string]any{"token": "Bot " + c.token, "intents": discordgo.IntentsGuilds | discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages | discordgo.IntentsMessageContent, "properties": map[string]string{"os": "swarm", "browser": "swarm", "device": "swarm"}}}
	}
	if err = wsjson.Write(connCtx, conn, identify); err != nil {
		return err
	}
	first := true
	for {
		var frame gatewayFrame
		readCtx := connCtx
		cancelRead := func() {}
		if first {
			readCtx, cancelRead = context.WithTimeout(connCtx, 15*time.Second)
		}
		err = wsjson.Read(readCtx, conn, &frame)
		cancelRead()
		if err != nil {
			return err
		}
		if frame.Sequence != nil {
			*seq = *frame.Sequence
			current.Store(*seq)
			sequenceKnown.Store(true)
		}
		switch frame.Op {
		case 11:
			ack.Store(true)
		case 1:
			if err = wsjson.Write(connCtx, conn, map[string]any{"op": 1, "d": heartbeatData()}); err != nil {
				return err
			}
		case 7:
			return errors.New("discord: reconnect requested")
		case 9:
			var resumable bool
			_ = json.Unmarshal(frame.Data, &resumable)
			if !resumable {
				*sessionID = ""
				*resumeURL = ""
				*seq = 0
			}
			return errors.New("discord: invalid gateway session")
		case 0:
			switch frame.Type {
			case "READY":
				var ready discordgo.Ready
				if json.Unmarshal(frame.Data, &ready) != nil || ready.SessionID == "" {
					return errors.New("discord: invalid ready")
				}
				*sessionID = ready.SessionID
				var details struct {
					ResumeURL string `json:"resume_gateway_url"`
				}
				_ = json.Unmarshal(frame.Data, &details)
				*resumeURL = details.ResumeURL
				first = false
				onReady()
				if c.OnReady != nil {
					c.OnReady(&ready)
				}
			case "RESUMED":
				first = false
				onReady()
			case "MESSAGE_CREATE":
				var msg discordgo.MessageCreate
				if json.Unmarshal(frame.Data, &msg) == nil && c.OnMessage != nil {
					c.OnMessage(&msg)
				}
			case "MESSAGE_UPDATE":
				var msg discordgo.MessageUpdate
				if json.Unmarshal(frame.Data, &msg) == nil && c.OnMessageUpdate != nil {
					c.OnMessageUpdate(&msg)
				}
			case "MESSAGE_DELETE":
				var msg discordgo.MessageDelete
				if json.Unmarshal(frame.Data, &msg) == nil && c.OnMessageDelete != nil {
					c.OnMessageDelete(&msg)
				}
			case "INTERACTION_CREATE":
				var interaction discordgo.InteractionCreate
				if json.Unmarshal(frame.Data, &interaction) == nil && c.OnInteraction != nil {
					c.OnInteraction(&interaction)
				}
			}
		}
	}
}
func components(actions []Action) ([]discordgo.MessageComponent, error) {
	if len(actions) > 5 {
		return nil, errors.New("discord: too many action rows")
	}
	rows := make([]discordgo.MessageComponent, 0, len(actions))
	for _, action := range actions {
		var component discordgo.MessageComponent
		if len(action.Choices) == 0 {
			component = discordgo.Button{Label: action.Label, CustomID: action.ID, Style: discordgo.PrimaryButton}
		} else {
			if len(action.Choices) > 25 {
				return nil, errors.New("discord: too many choices")
			}
			options := make([]discordgo.SelectMenuOption, 0, len(action.Choices))
			for _, choice := range action.Choices {
				options = append(options, discordgo.SelectMenuOption{Label: choice.Label, Value: choice.ID, Description: choice.Description})
			}
			component = discordgo.SelectMenu{CustomID: action.ID, Placeholder: action.Label, Options: options}
		}
		rows = append(rows, discordgo.ActionsRow{Components: []discordgo.MessageComponent{component}})
	}
	return rows, nil
}
func (c *DiscordClient) Post(ctx context.Context, out Outbound) (string, error) {
	rows, err := components(out.Actions)
	if err != nil {
		return "", err
	}
	payload := struct {
		Content         string                            `json:"content"`
		Components      []discordgo.MessageComponent      `json:"components"`
		AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
		Nonce           string                            `json:"nonce"`
		EnforceNonce    bool                              `json:"enforce_nonce"`
	}{out.Text, rows, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}, out.Nonce, true}
	var msg discordgo.Message
	if err := c.REST(ctx, http.MethodPost, "/channels/"+url.PathEscape(out.Route.ChannelID)+"/messages", payload, &msg); err != nil {
		return "", err
	}
	if msg.ID == "" {
		return "", errors.New("discord: invalid post response")
	}

	return msg.ID, nil
}
func (c *DiscordClient) Edit(ctx context.Context, route Route, text string, actions []Action) (string, error) {
	rows, err := components(actions)
	if err != nil {
		return "", err
	}
	payload := struct {
		Content         string                            `json:"content"`
		Components      []discordgo.MessageComponent      `json:"components"`
		AllowedMentions *discordgo.MessageAllowedMentions `json:"allowed_mentions"`
	}{text, rows, &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}}
	var msg discordgo.Message
	if err := c.REST(ctx, http.MethodPatch, "/channels/"+url.PathEscape(route.ChannelID)+"/messages/"+url.PathEscape(route.MessageID), payload, &msg); err != nil {
		return "", err
	}
	if msg.ID == "" {
		return "", errors.New("discord: invalid edit response")
	}

	return msg.ID, nil
}
func (c *DiscordClient) Message(ctx context.Context, route Route) (Input, error) {
	var msg discordgo.Message
	if err := c.REST(ctx, http.MethodGet, "/channels/"+url.PathEscape(route.ChannelID)+"/messages/"+url.PathEscape(route.MessageID), nil, &msg); err != nil {
		return Input{}, err
	}

	if msg.Author == nil || msg.ID != route.MessageID || msg.ChannelID != route.ChannelID || (msg.GuildID != "" && msg.GuildID != route.GuildID) {
		return Input{}, errors.New("discord: invalid message response")
	}
	return Input{EventID: msg.ID, Actor: Actor{ID: msg.Author.ID, Name: msg.Author.Username}, Route: route, Text: msg.Content, Bot: msg.Author.Bot, At: msg.Timestamp}, nil
}
