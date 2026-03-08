// Package wsocket provides a Go SDK for the wSocket realtime pub/sub platform.
//
// Usage:
//
//	client := wsocket.NewClient("ws://localhost:9001", "your-api-key")
//	err := client.Connect()
//	defer client.Disconnect()
//
//	ch := client.Channel("chat:general")
//	ch.Subscribe(func(data map[string]any, meta wsocket.MessageMeta) {
//	    fmt.Printf("[%s] %v\n", meta.Channel, data)
//	})
//	ch.Publish(map[string]any{"text": "Hello from Go!"})
//
//	// Presence
//	ch.Presence().Enter(map[string]any{"name": "Alice"})
//	ch.Presence().OnEnter(func(m wsocket.PresenceMember) { fmt.Println("joined:", m.ClientID) })
//	ch.Presence().Get()
//
//	// History
//	ch.OnHistory(func(result wsocket.HistoryResult) { fmt.Println(result.Messages) })
//	ch.History(wsocket.HistoryOptions{Limit: 50})
package wsocket

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ─── Types ──────────────────────────────────────────────────

// MessageMeta carries metadata for each received message.
type MessageMeta struct {
	ID        string `json:"id"`
	Channel   string `json:"channel"`
	Timestamp int64  `json:"timestamp"`
}

// PresenceMember represents a member in a channel's presence set.
type PresenceMember struct {
	ClientID string         `json:"clientId"`
	Data     map[string]any `json:"data,omitempty"`
	JoinedAt int64          `json:"joinedAt"`
}

// HistoryMessage represents a single message from history.
type HistoryMessage struct {
	ID          string `json:"id"`
	Channel     string `json:"channel"`
	Data        any    `json:"data"`
	PublisherID string `json:"publisherId"`
	Timestamp   int64  `json:"timestamp"`
	Sequence    int64  `json:"sequence"`
}

// HistoryResult is the result of a history query.
type HistoryResult struct {
	Channel  string           `json:"channel"`
	Messages []HistoryMessage `json:"messages"`
	HasMore  bool             `json:"hasMore"`
}

// HistoryOptions configures a history query.
type HistoryOptions struct {
	Limit     int    `json:"limit,omitempty"`
	Before    int64  `json:"before,omitempty"`
	After     int64  `json:"after,omitempty"`
	Direction string `json:"direction,omitempty"` // "forward" or "backward"
}

// PublishOptions configures a publish call.
type PublishOptions struct {
	Persist *bool `json:"persist,omitempty"`
}

// ClientOptions configures the wSocket client.
type ClientOptions struct {
	AutoReconnect        bool
	MaxReconnectAttempts int
	ReconnectDelay       time.Duration
	Token                string
	Recover              bool
}

// DefaultOptions returns sensible defaults.
func DefaultOptions() ClientOptions {
	return ClientOptions{
		AutoReconnect:        true,
		MaxReconnectAttempts: 10,
		ReconnectDelay:       time.Second,
		Recover:              true,
	}
}

// MessageCallback is called when a message is received.
type MessageCallback func(data map[string]any, meta MessageMeta)

// PresenceCallback is called for a single presence member event.
type PresenceCallback func(member PresenceMember)

// MembersCallback is called with the full list of presence members.
type MembersCallback func(members []PresenceMember)

// HistoryCallback is called with the result of a history query.
type HistoryCallback func(result HistoryResult)

// ─── Presence ───────────────────────────────────────────────

// Presence provides the presence API for a channel.
type Presence struct {
	channelName string
	sendFn      func(msg map[string]any)
	enterCbs    []PresenceCallback
	leaveCbs    []PresenceCallback
	updateCbs   []PresenceCallback
	membersCbs  []MembersCallback
	mu          sync.Mutex
}

func newPresence(channelName string, sendFn func(msg map[string]any)) *Presence {
	return &Presence{channelName: channelName, sendFn: sendFn}
}

// Enter joins the channel's presence set with optional data.
func (p *Presence) Enter(data map[string]any) *Presence {
	msg := map[string]any{"action": "presence.enter", "channel": p.channelName}
	if data != nil {
		msg["data"] = data
	}
	p.sendFn(msg)
	return p
}

// Leave exits the channel's presence set.
func (p *Presence) Leave() *Presence {
	p.sendFn(map[string]any{"action": "presence.leave", "channel": p.channelName})
	return p
}

// Update modifies presence data.
func (p *Presence) Update(data map[string]any) *Presence {
	p.sendFn(map[string]any{"action": "presence.update", "channel": p.channelName, "data": data})
	return p
}

// Get requests the current member list.
func (p *Presence) Get() *Presence {
	p.sendFn(map[string]any{"action": "presence.get", "channel": p.channelName})
	return p
}

// OnEnter registers a callback for members entering.
func (p *Presence) OnEnter(cb PresenceCallback) *Presence {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.enterCbs = append(p.enterCbs, cb)
	return p
}

// OnLeave registers a callback for members leaving.
func (p *Presence) OnLeave(cb PresenceCallback) *Presence {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leaveCbs = append(p.leaveCbs, cb)
	return p
}

// OnUpdate registers a callback for presence data updates.
func (p *Presence) OnUpdate(cb PresenceCallback) *Presence {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.updateCbs = append(p.updateCbs, cb)
	return p
}

// OnMembers registers a callback for the full member list response.
func (p *Presence) OnMembers(cb MembersCallback) *Presence {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.membersCbs = append(p.membersCbs, cb)
	return p
}

func (p *Presence) emitEnter(m PresenceMember) {
	p.mu.Lock()
	cbs := make([]PresenceCallback, len(p.enterCbs))
	copy(cbs, p.enterCbs)
	p.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(m) }()
	}
}

func (p *Presence) emitLeave(m PresenceMember) {
	p.mu.Lock()
	cbs := make([]PresenceCallback, len(p.leaveCbs))
	copy(cbs, p.leaveCbs)
	p.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(m) }()
	}
}

func (p *Presence) emitUpdate(m PresenceMember) {
	p.mu.Lock()
	cbs := make([]PresenceCallback, len(p.updateCbs))
	copy(cbs, p.updateCbs)
	p.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(m) }()
	}
}

func (p *Presence) emitMembers(members []PresenceMember) {
	p.mu.Lock()
	cbs := make([]MembersCallback, len(p.membersCbs))
	copy(cbs, p.membersCbs)
	p.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(members) }()
	}
}

// ─── Channel ────────────────────────────────────────────────

// Channel represents a pub/sub channel.
type Channel struct {
	Name       string
	sendFn     func(msg map[string]any)
	subscribed bool
	callbacks  []MessageCallback
	historyCbs []HistoryCallback
	presence   *Presence
	mu         sync.Mutex
}

// Presence returns the Presence API for this channel.
func (ch *Channel) Presence() *Presence { return ch.presence }

// Subscribe registers a callback for messages on this channel.
func (ch *Channel) Subscribe(cb MessageCallback) *Channel {
	return ch.SubscribeWithRewind(cb, 0)
}

// SubscribeWithRewind registers a callback with optional rewind.
func (ch *Channel) SubscribeWithRewind(cb MessageCallback, rewind int) *Channel {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.callbacks = append(ch.callbacks, cb)
	if !ch.subscribed {
		msg := map[string]any{"action": "subscribe", "channel": ch.Name}
		if rewind > 0 {
			msg["rewind"] = rewind
		}
		ch.sendFn(msg)
		ch.subscribed = true
	}
	return ch
}

// Publish sends data to this channel.
func (ch *Channel) Publish(data any) *Channel {
	return ch.PublishWithOptions(data, PublishOptions{})
}

// PublishWithOptions sends data with custom options.
func (ch *Channel) PublishWithOptions(data any, opts PublishOptions) *Channel {
	msg := map[string]any{"action": "publish", "channel": ch.Name, "data": data, "id": generateID()}
	if opts.Persist != nil && !*opts.Persist {
		msg["persist"] = false
	}
	ch.sendFn(msg)
	return ch
}

// History queries the message history for this channel.
func (ch *Channel) History(opts HistoryOptions) *Channel {
	msg := map[string]any{"action": "history", "channel": ch.Name}
	if opts.Limit > 0 {
		msg["limit"] = opts.Limit
	}
	if opts.Before > 0 {
		msg["before"] = opts.Before
	}
	if opts.After > 0 {
		msg["after"] = opts.After
	}
	if opts.Direction != "" {
		msg["direction"] = opts.Direction
	}
	ch.sendFn(msg)
	return ch
}

// OnHistory registers a callback for history query results.
func (ch *Channel) OnHistory(cb HistoryCallback) *Channel {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.historyCbs = append(ch.historyCbs, cb)
	return ch
}

// Unsubscribe removes all listeners and unsubscribes from the channel.
func (ch *Channel) Unsubscribe() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.sendFn(map[string]any{"action": "unsubscribe", "channel": ch.Name})
	ch.subscribed = false
	ch.callbacks = nil
	ch.historyCbs = nil
}

func (ch *Channel) emit(data map[string]any, meta MessageMeta) {
	ch.mu.Lock()
	cbs := make([]MessageCallback, len(ch.callbacks))
	copy(cbs, ch.callbacks)
	ch.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(data, meta) }()
	}
}

func (ch *Channel) emitHistory(result HistoryResult) {
	ch.mu.Lock()
	cbs := make([]HistoryCallback, len(ch.historyCbs))
	copy(cbs, ch.historyCbs)
	ch.mu.Unlock()
	for _, cb := range cbs {
		func() { defer func() { recover() }(); cb(result) }()
	}
}

func (ch *Channel) markForResubscribe() { ch.mu.Lock(); ch.subscribed = false; ch.mu.Unlock() }

func (ch *Channel) hasListeners() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return len(ch.callbacks) > 0
}

// ─── PubSub Namespace ──────────────────────────────────────

// PubSubNamespace provides a scoped API for pub/sub operations.
type PubSubNamespace struct {
	client *Client
}

// Channel returns (or creates) a channel reference (same as client.Channel()).
func (ns *PubSubNamespace) Channel(name string) *Channel {
	return ns.client.Channel(name)
}

// ─── Client ─────────────────────────────────────────────────

// Client is the main wSocket client.
type Client struct {
	url               string
	apiKey            string
	options           ClientOptions
	conn              *websocket.Conn
	channels          map[string]*Channel
	mu                sync.RWMutex
	done              chan struct{}
	reconnectAttempts int
	lastMessageTs     int64
	resumeToken       string
	pingDone          chan struct{}

	// PubSub namespace — client.PubSub.Channel("name")
	PubSub *PubSubNamespace
	// Push namespace — set via client.ConfigurePush()
	Push *PushClient
}

// NewClient creates a new wSocket client.
func NewClient(serverURL, apiKey string, opts ...ClientOptions) *Client {
	o := DefaultOptions()
	if len(opts) > 0 {
		o = opts[0]
	}
	c := &Client{url: serverURL, apiKey: apiKey, options: o, channels: make(map[string]*Channel)}
	c.PubSub = &PubSubNamespace{client: c}
	return c
}

// ConfigurePush sets up push notification access.
func (c *Client) ConfigurePush(baseURL, token, appID string) *PushClient {
	c.Push = NewPushClient(baseURL, token, appID)
	return c.Push
}

// Connect establishes the WebSocket connection.
func (c *Client) Connect() error {
	u, err := url.Parse(c.url)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	if c.options.Token != "" {
		q.Set("token", c.options.Token)
	} else {
		q.Set("key", c.apiKey)
	}
	u.RawQuery = q.Encode()
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	c.conn = conn
	c.done = make(chan struct{})
	c.pingDone = make(chan struct{})
	c.reconnectAttempts = 0
	go c.readLoop()
	go c.pingLoop()
	return nil
}

// Disconnect closes the connection.
func (c *Client) Disconnect() {
	if c.pingDone != nil {
		close(c.pingDone)
	}
	if c.done != nil {
		close(c.done)
	}
	if c.conn != nil {
		c.conn.Close()
	}
}

// Channel returns (or creates) a channel reference.
func (c *Client) Channel(name string) *Channel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.channels[name]; ok {
		return ch
	}
	ch := &Channel{Name: name, sendFn: c.send, presence: newPresence(name, c.send)}
	c.channels[name] = ch
	return ch
}

func (c *Client) send(msg map[string]any) {
	if c.conn == nil {
		return
	}
	data, _ := json.Marshal(msg)
	c.conn.WriteMessage(websocket.TextMessage, data)
}

func (c *Client) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.pingDone:
			return
		case <-ticker.C:
			c.send(map[string]any{"action": "ping"})
		}
	}
}

func (c *Client) readLoop() {
	for {
		select {
		case <-c.done:
			return
		default:
		}
		_, raw, err := c.conn.ReadMessage()
		if err != nil {
			log.Printf("[wSocket] Read error: %v", err)
			c.handleDisconnect()
			return
		}
		var msg map[string]any
		if json.Unmarshal(raw, &msg) != nil {
			continue
		}
		c.handleMessage(msg)
	}
}

func (c *Client) handleMessage(msg map[string]any) {
	action, _ := msg["action"].(string)
	channel, _ := msg["channel"].(string)

	switch action {
	case "message":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			data, _ := msg["data"].(map[string]any)
			id, _ := msg["id"].(string)
			ts, _ := msg["timestamp"].(float64)
			tsInt := int64(ts)
			if tsInt > c.lastMessageTs {
				c.lastMessageTs = tsInt
			}
			ch.emit(data, MessageMeta{ID: id, Channel: channel, Timestamp: tsInt})
		}
	case "presence.enter":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			ch.presence.emitEnter(parsePresenceMember(msg["data"]))
		}
	case "presence.leave":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			ch.presence.emitLeave(parsePresenceMember(msg["data"]))
		}
	case "presence.update":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			ch.presence.emitUpdate(parsePresenceMember(msg["data"]))
		}
	case "presence.members":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			ch.presence.emitMembers(parsePresenceMembers(msg["data"]))
		}
	case "history":
		c.mu.RLock()
		ch, ok := c.channels[channel]
		c.mu.RUnlock()
		if ok {
			ch.emitHistory(parseHistoryResult(msg["data"], channel))
		}
	case "ack":
		if id, _ := msg["id"].(string); id == "resume" {
			if data, ok := msg["data"].(map[string]any); ok {
				if token, ok := data["resumeToken"].(string); ok {
					c.resumeToken = token
				}
			}
		}
	case "error":
		errMsg, _ := msg["error"].(string)
		log.Printf("[wSocket] Error: %s", errMsg)
	}
}

func (c *Client) handleDisconnect() {
	if !c.options.AutoReconnect {
		return
	}
	for c.reconnectAttempts < c.options.MaxReconnectAttempts {
		c.reconnectAttempts++
		delay := c.options.ReconnectDelay * time.Duration(1<<uint(c.reconnectAttempts-1))
		log.Printf("[wSocket] Reconnecting in %v (attempt %d/%d)", delay, c.reconnectAttempts, c.options.MaxReconnectAttempts)
		time.Sleep(delay)
		if err := c.Connect(); err == nil {
			c.resubscribeAll()
			return
		}
	}
	log.Printf("[wSocket] Max reconnect attempts reached")
}

func (c *Client) resubscribeAll() {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.options.Recover && c.lastMessageTs > 0 {
		names := []string{}
		for _, ch := range c.channels {
			if ch.hasListeners() {
				names = append(names, ch.Name)
				ch.markForResubscribe()
			}
		}
		if len(names) > 0 {
			token := c.resumeToken
			if token == "" {
				payload, _ := json.Marshal(map[string]any{"channels": names, "lastTs": c.lastMessageTs})
				token = base64.URLEncoding.EncodeToString(payload)
			}
			c.send(map[string]any{"action": "resume", "resumeToken": token})
			return
		}
	}
	for _, ch := range c.channels {
		ch.markForResubscribe()
		if ch.hasListeners() {
			c.send(map[string]any{"action": "subscribe", "channel": ch.Name})
			ch.mu.Lock()
			ch.subscribed = true
			ch.mu.Unlock()
		}
	}
}

// ─── Helpers ────────────────────────────────────────────────

func parsePresenceMember(data any) PresenceMember {
	m := PresenceMember{}
	if dm, ok := data.(map[string]any); ok {
		m.ClientID, _ = dm["clientId"].(string)
		if d, ok := dm["data"].(map[string]any); ok {
			m.Data = d
		}
		if jt, ok := dm["joinedAt"].(float64); ok {
			m.JoinedAt = int64(jt)
		}
	}
	return m
}

func parsePresenceMembers(data any) []PresenceMember {
	var members []PresenceMember
	if arr, ok := data.([]any); ok {
		for _, item := range arr {
			members = append(members, parsePresenceMember(item))
		}
	}
	return members
}

func parseHistoryResult(data any, channel string) HistoryResult {
	result := HistoryResult{Channel: channel}
	dm, ok := data.(map[string]any)
	if !ok {
		return result
	}
	if ch, ok := dm["channel"].(string); ok {
		result.Channel = ch
	}
	result.HasMore, _ = dm["hasMore"].(bool)
	if msgs, ok := dm["messages"].([]any); ok {
		for _, item := range msgs {
			if m, ok := item.(map[string]any); ok {
				hm := HistoryMessage{}
				hm.ID, _ = m["id"].(string)
				hm.Channel, _ = m["channel"].(string)
				hm.Data = m["data"]
				hm.PublisherID, _ = m["publisherId"].(string)
				if ts, ok := m["timestamp"].(float64); ok {
					hm.Timestamp = int64(ts)
				}
				if seq, ok := m["sequence"].(float64); ok {
					hm.Sequence = int64(seq)
				}
				result.Messages = append(result.Messages, hm)
			}
		}
	}
	return result
}

func generateID() string {
	return fmt.Sprintf("%d-%x", time.Now().UnixMilli(), rand.Int63())
}

// ─── Push Notifications ─────────────────────────────────────

// PushClient provides REST-based push notification operations.
type PushClient struct {
	BaseURL string
	Token   string
	AppID   string
	client  *http.Client
}

// PushPayload represents a push notification message.
type PushPayload struct {
	Title   string                 `json:"title"`
	Body    string                 `json:"body,omitempty"`
	Icon    string                 `json:"icon,omitempty"`
	Image   string                 `json:"image,omitempty"`
	Badge   string                 `json:"badge,omitempty"`
	URL     string                 `json:"url,omitempty"`
	Data    map[string]interface{} `json:"data,omitempty"`
	TTL     int                    `json:"ttl,omitempty"`
	Urgency string                 `json:"urgency,omitempty"`
}

// NewPushClient creates a new push notification client.
func NewPushClient(baseURL, token, appID string) *PushClient {
	return &PushClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		AppID:   appID,
		client:  &http.Client{Timeout: 30 * time.Second},
	}
}

func (p *PushClient) apiRequest(method, path string, body interface{}) (map[string]interface{}, error) {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, p.BaseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Token)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("push API error %d: %s", resp.StatusCode, string(respBody))
	}
	var result map[string]interface{}
	json.Unmarshal(respBody, &result)
	return result, nil
}

// RegisterFCM registers an FCM device token (Android).
func (p *PushClient) RegisterFCM(deviceToken string, memberID string) (string, error) {
	res, err := p.apiRequest("POST", fmt.Sprintf("/api/admin/apps/%s/push/register", p.AppID), map[string]interface{}{
		"platform":    "fcm",
		"memberId":    memberID,
		"deviceToken": deviceToken,
	})
	if err != nil {
		return "", err
	}
	return res["subscriptionId"].(string), nil
}

// RegisterAPNs registers an APNs device token (iOS).
func (p *PushClient) RegisterAPNs(deviceToken string, memberID string) (string, error) {
	res, err := p.apiRequest("POST", fmt.Sprintf("/api/admin/apps/%s/push/register", p.AppID), map[string]interface{}{
		"platform":    "apns",
		"memberId":    memberID,
		"deviceToken": deviceToken,
	})
	if err != nil {
		return "", err
	}
	return res["subscriptionId"].(string), nil
}

// Unregister removes push subscriptions for a member.
func (p *PushClient) Unregister(memberID string, platform string) (int, error) {
	body := map[string]interface{}{"memberId": memberID}
	if platform != "" {
		body["platform"] = platform
	}
	res, err := p.apiRequest("DELETE", fmt.Sprintf("/api/admin/apps/%s/push/unregister", p.AppID), body)
	if err != nil {
		return 0, err
	}
	if removed, ok := res["removed"].(float64); ok {
		return int(removed), nil
	}
	return 0, nil
}

// DeleteSubscription removes a specific push subscription by its ID.
func (p *PushClient) DeleteSubscription(subscriptionID string) (bool, error) {
	res, err := p.apiRequest("DELETE", fmt.Sprintf("/api/admin/apps/%s/push/subscriptions/%s", p.AppID, subscriptionID), nil)
	if err != nil {
		return false, err
	}
	if deleted, ok := res["deleted"].(bool); ok {
		return deleted, nil
	}
	return false, nil
}

// SendToMember sends a push notification to a specific member.
func (p *PushClient) SendToMember(memberID string, payload PushPayload) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"memberId": memberID,
		"title":    payload.Title,
		"body":     payload.Body,
	}
	if payload.Icon != "" {
		body["icon"] = payload.Icon
	}
	if payload.URL != "" {
		body["url"] = payload.URL
	}
	if payload.Data != nil {
		body["data"] = payload.Data
	}
	return p.apiRequest("POST", fmt.Sprintf("/api/admin/apps/%s/push/send", p.AppID), body)
}

// Broadcast sends a push notification to all app subscribers.
func (p *PushClient) Broadcast(payload PushPayload) (map[string]interface{}, error) {
	body := map[string]interface{}{
		"broadcast": true,
		"title":     payload.Title,
		"body":      payload.Body,
	}
	if payload.Icon != "" {
		body["icon"] = payload.Icon
	}
	if payload.URL != "" {
		body["url"] = payload.URL
	}
	if payload.Data != nil {
		body["data"] = payload.Data
	}
	return p.apiRequest("POST", fmt.Sprintf("/api/admin/apps/%s/push/send", p.AppID), body)
}
