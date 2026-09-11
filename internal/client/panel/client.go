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

// ClientTraffic is live usage data returned by 3x-ui; it is not persisted locally.
type ClientTraffic struct {
	Up         int64 `json:"up"`
	Down       int64 `json:"down"`
	Total      int64 `json:"total"`
	ExpiryTime int64 `json:"expiryTime"`
	Enable     bool  `json:"enable"`
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
func (c *Client) AddClient(inboundID int, email, uuid, subID string, expiryTimeMs, totalBytes int64) error {
	return c.AddClientContext(context.Background(), inboundID, email, uuid, subID, expiryTimeMs, totalBytes)
}

func (c *Client) AddClientContext(ctx context.Context, inboundID int, email, uuid, subID string, expiryTimeMs, totalBytes int64) error {
	settings, err := json.Marshal(struct {
		Clients []struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			ExpiryTime int64  `json:"expiryTime"`
			TotalGB    int64  `json:"totalGB"`
			SubID      string `json:"subId"`
			Enable     bool   `json:"enable"`
		} `json:"clients"`
	}{Clients: []struct {
		ID         string `json:"id"`
		Email      string `json:"email"`
		ExpiryTime int64  `json:"expiryTime"`
		TotalGB    int64  `json:"totalGB"`
		SubID      string `json:"subId"`
		Enable     bool   `json:"enable"`
	}{{ID: uuid, Email: email, ExpiryTime: expiryTimeMs, TotalGB: totalBytes, SubID: subID, Enable: true}}})
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

func (c *Client) GetClientTrafficsContext(ctx context.Context, email string) (*ClientTraffic, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf("authenticate client traffics request: %w", err)
	}
	resp, err := c.sendGet(ctx, "/panel/api/inbounds/getClientTraffics/"+url.PathEscape(email))
	if err != nil {
		return nil, fmt.Errorf("get client traffics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, responseError("get client traffics", resp)
	}
	var result struct {
		Success bool          `json:"success"`
		Msg     string        `json:"msg"`
		Obj     ClientTraffic `json:"obj"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode client traffics: %w", err)
	}
	if !result.Success {
		return nil, fmt.Errorf("get client traffics: panel returned unsuccessful response: %s", result.Msg)
	}
	return &result.Obj, nil
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
