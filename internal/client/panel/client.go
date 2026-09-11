// Package panel implements the HTTP client for the 3x-ui panel API.
package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const sessionCookieName = "session"

// Client is safe for concurrent use.
type Client struct {
	baseURL       string
	username      string
	password      string
	sessionCookie string
	mu            sync.RWMutex
	httpClient    *http.Client
}

// InboundSettings contains the values required to build a Reality VLESS URL.
type InboundSettings struct {
	ServerIP   string
	ServerPort int
	PublicKey  string
	SNI        string
	ShortID    string
}

func NewClient(baseURL, username, password string) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		username:   username,
		password:   password,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// Login authenticates with the panel using context.Background.
// Prefer LoginContext when a request context is available.
func (c *Client) Login() error {
	return c.LoginContext(context.Background())
}

func (c *Client) LoginContext(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loginLocked(ctx)
}

func (c *Client) loginLocked(ctx context.Context) error {
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("login: unexpected HTTP status %s", resp.Status)
	}

	for _, cookie := range resp.Cookies() {
		if cookie.Name == sessionCookieName {
			c.sessionCookie = cookie.Value
			log.Printf("panel: authenticated successfully")
			return nil
		}
	}
	return fmt.Errorf("login: session cookie missing in response")
}

// AddClient adds a VLESS client to an inbound using context.Background.
func (c *Client) AddClient(inboundID int, email, uuid string, expiryTimeMs int64) error {
	return c.AddClientContext(context.Background(), inboundID, email, uuid, expiryTimeMs)
}

func (c *Client) AddClientContext(ctx context.Context, inboundID int, email, uuid string, expiryTimeMs int64) error {
	settings, err := json.Marshal(struct {
		Clients []struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			ExpiryTime int64  `json:"expiryTime"`
			Enable     bool   `json:"enable"`
		} `json:"clients"`
	}{Clients: []struct {
		ID         string `json:"id"`
		Email      string `json:"email"`
		ExpiryTime int64  `json:"expiryTime"`
		Enable     bool   `json:"enable"`
	}{{ID: uuid, Email: email, ExpiryTime: expiryTimeMs, Enable: true}}})
	if err != nil {
		return fmt.Errorf("marshal add client settings: %w", err)
	}

	body, err := json.Marshal(struct {
		ID       int    `json:"id"`
		Settings string `json:"settings"`
	}{ID: inboundID, Settings: string(settings)})
	if err != nil {
		return fmt.Errorf("marshal add client request: %w", err)
	}
	return c.doJSON(ctx, "/panel/api/inbounds/addClient", body)
}

// DeleteClient deletes an inbound client by its unique email using context.Background.
func (c *Client) DeleteClient(inboundID int, email string) error {
	return c.DeleteClientContext(context.Background(), inboundID, email)
}

func (c *Client) DeleteClientContext(ctx context.Context, inboundID int, email string) error {
	body, err := json.Marshal(struct {
		ID    int    `json:"id"`
		Email string `json:"email"`
	}{ID: inboundID, Email: email})
	if err != nil {
		return fmt.Errorf("marshal delete client request: %w", err)
	}
	return c.doJSON(ctx, "/panel/api/inbounds/delClient", body)
}

// GetInboundSettings reads Reality connection settings using context.Background.
func (c *Client) GetInboundSettings(inboundID int) (*InboundSettings, error) {
	return c.GetInboundSettingsContext(context.Background(), inboundID)
}

func (c *Client) GetInboundSettingsContext(ctx context.Context, inboundID int) (*InboundSettings, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf("authenticate get inbound settings request: %w", err)
	}

	resp, err := c.sendGet(ctx, fmt.Sprintf("/panel/api/inbounds/get/%d", inboundID))
	if err != nil {
		return nil, fmt.Errorf("send get inbound settings request: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		c.mu.Lock()
		err = c.loginLocked(ctx)
		c.mu.Unlock()
		if err != nil {
			return nil, fmt.Errorf("refresh panel session: %w", err)
		}
		resp, err = c.sendGet(ctx, fmt.Sprintf("/panel/api/inbounds/get/%d", inboundID))
		if err != nil {
			return nil, fmt.Errorf("retry get inbound settings request: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, responseError("get inbound settings", resp)
	}

	var response struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
		Obj     struct {
			Port           int             `json:"port"`
			StreamSettings json.RawMessage `json:"streamSettings"`
		} `json:"obj"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("decode inbound settings response: %w", err)
	}
	if !response.Success {
		return nil, fmt.Errorf("get inbound settings: panel returned unsuccessful response: %s", response.Msg)
	}

	var stream struct {
		RealitySettings struct {
			PublicKey   string   `json:"publicKey"`
			ServerNames []string `json:"serverNames"`
			ShortIDs    []string `json:"shortIds"`
		} `json:"realitySettings"`
	}
	if err := json.Unmarshal(response.Obj.StreamSettings, &stream); err != nil {
		return nil, fmt.Errorf("decode reality settings: %w", err)
	}
	if len(stream.RealitySettings.ServerNames) == 0 || len(stream.RealitySettings.ShortIDs) == 0 || stream.RealitySettings.PublicKey == "" {
		return nil, fmt.Errorf("get inbound settings: incomplete Reality settings")
	}
	panelURL, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse panel URL for inbound settings: %w", err)
	}
	if panelURL.Hostname() == "" {
		return nil, fmt.Errorf("get inbound settings: panel URL does not contain a host")
	}
	return &InboundSettings{ServerIP: panelURL.Hostname(), ServerPort: response.Obj.Port, PublicKey: stream.RealitySettings.PublicKey, SNI: stream.RealitySettings.ServerNames[0], ShortID: stream.RealitySettings.ShortIDs[0]}, nil
}

func (c *Client) ensureAuth(ctx context.Context) error {
	c.mu.RLock()
	hasSession := c.sessionCookie != ""
	c.mu.RUnlock()
	if hasSession {
		return nil
	}
	return c.LoginContext(ctx)
}

func (c *Client) doJSON(ctx context.Context, path string, body []byte) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf("authenticate panel request: %w", err)
	}

	resp, err := c.sendJSON(ctx, path, body)
	if err != nil {
		return fmt.Errorf("send panel request: %w", err)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		c.mu.Lock()
		err := c.loginLocked(ctx)
		c.mu.Unlock()
		if err != nil {
			return fmt.Errorf("refresh panel session: %w", err)
		}
		resp, err = c.sendJSON(ctx, path, body)
		if err != nil {
			return fmt.Errorf("retry panel request: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError("panel request", resp)
	}
	return nil
}

func (c *Client) sendJSON(ctx context.Context, path string, body []byte) (*http.Response, error) {
	c.mu.RLock()
	session := c.sessionCookie
	c.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", sessionCookieName+"="+session)
	return c.httpClient.Do(req)
}

func (c *Client) sendGet(ctx context.Context, path string) (*http.Response, error) {
	c.mu.RLock()
	session := c.sessionCookie
	c.mu.RUnlock()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Cookie", sessionCookieName+"="+session)
	return c.httpClient.Do(req)
}

func responseError(operation string, resp *http.Response) error {
	const maxBody = 4 << 10
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if len(body) == 0 {
		return fmt.Errorf("%s: unexpected HTTP status %s", operation, resp.Status)
	}
	return fmt.Errorf("%s: unexpected HTTP status %s: %s", operation, resp.Status, strings.TrimSpace(string(body)))
}
