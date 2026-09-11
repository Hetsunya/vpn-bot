package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	username   string
	password   string
	csrfToken  string
	mu         sync.RWMutex
	httpClient *http.Client
}

type InboundSettings struct {
	ServerIP   string
	ServerPort int
	PublicKey  string
	SNI        string
	ShortID    string
}

func NewClient(baseURL, username, password string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		httpClient: &http.Client{
			Jar:     jar,
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) Login() error {
	return c.LoginContext(context.Background())
}

func (c *Client) LoginContext(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loginLocked(ctx)
}

func (c *Client) loginLocked(ctx context.Context) error {
	// Шаг 1: GET / для получения CSRF-токена и начальных cookies
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/", nil)
	if err != nil {
		return fmt.Errorf("create get request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("get request: %w", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Извлекаем CSRF-токен из HTML
	re := regexp.MustCompile(`<meta\s+name="csrf-token"\s+content="([^"]+)"`)
	matches := re.FindSubmatch(body)
	if len(matches) < 2 {
		return fmt.Errorf("csrf token not found in panel response")
	}
	c.csrfToken = string(matches[1])

	// Шаг 2: POST /login с CSRF-токеном и учетными данными
	formData := strings.NewReader(fmt.Sprintf("username=%s&password=%s", c.username, c.password))
	req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", formData)
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", c.csrfToken)

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login: unexpected HTTP status %s", resp.Status)
	}

	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("decode login response: %w", err)
	}

	if !result.Success {
		return fmt.Errorf("login failed: %s", result.Msg)
	}

	log.Println("panel: authenticated successfully")
	return nil
}

func (c *Client) ensureAuth(ctx context.Context) error {
	c.mu.RLock()
	// Простая проверка: если токен есть, считаем, что авторизованы.
	// В случае 401/403 вызывающий метод сам вызовет LoginContext.
	hasSession := c.csrfToken != ""
	c.mu.RUnlock()

	if !hasSession {
		return c.LoginContext(ctx)
	}
	return nil
}

func (c *Client) AddClient(inboundID int, email, uuid string, expiryTimeMs int64) error {
	return c.AddClientContext(context.Background(), inboundID, email, uuid, expiryTimeMs)
}

func (c *Client) AddClientContext(ctx context.Context, inboundID int, email, uuid string, expiryTimeMs int64) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf("authenticate panel request: %w", err)
	}

	settings, err := json.Marshal(struct {
		Clients []struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			ExpiryTime int64  `json:"expiryTime"`
			Enable     bool   `json:"enable"`
		} `json:"clients"`
	}{
		Clients: []struct {
			ID         string `json:"id"`
			Email      string `json:"email"`
			ExpiryTime int64  `json:"expiryTime"`
			Enable     bool   `json:"enable"`
		}{{ID: uuid, Email: email, ExpiryTime: expiryTimeMs, Enable: true}},
	})
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

	return c.doJSONWithRetry(ctx, "/panel/api/inbounds/addClient", body)
}

func (c *Client) DeleteClient(inboundID int, email string) error {
	return c.DeleteClientContext(context.Background(), inboundID, email)
}

func (c *Client) DeleteClientContext(ctx context.Context, inboundID int, email string) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf("authenticate panel request: %w", err)
	}

	body, err := json.Marshal(struct {
		ID    int    `json:"id"`
		Email string `json:"email"`
	}{ID: inboundID, Email: email})
	if err != nil {
		return fmt.Errorf("marshal delete client request: %w", err)
	}

	return c.doJSONWithRetry(ctx, "/panel/api/inbounds/delClient", body)
}

func (c *Client) GetInboundSettings(inboundID int) (*InboundSettings, error) {
	return c.GetInboundSettingsContext(context.Background(), inboundID)
}

func (c *Client) GetInboundSettingsContext(ctx context.Context, inboundID int) (*InboundSettings, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf("authenticate get inbound settings request: %w", err)
	}

	resp, err := c.sendGet(ctx, fmt.Sprintf("/panel/api/inbounds/get/%d", inboundID))
	if err != nil {
		return nil, err
	}

	// Если сессия протухла (401/403), обновляем и повторяем
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		if err := c.LoginContext(ctx); err != nil {
			return nil, fmt.Errorf("refresh panel session: %w", err)
		}
		resp, err = c.sendGet(ctx, fmt.Sprintf("/panel/api/inbounds/get/%d", inboundID))
		if err != nil {
			return nil, err
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
		return nil, fmt.Errorf("get inbound settings: %s", response.Msg)
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
		return nil, fmt.Errorf("incomplete Reality settings")
	}

	return &InboundSettings{
		ServerIP:   strings.Split(c.baseURL, "://")[1], // Упрощенное извлечение хоста
		ServerPort: response.Obj.Port,
		PublicKey:  stream.RealitySettings.PublicKey,
		SNI:        stream.RealitySettings.ServerNames[0],
		ShortID:    stream.RealitySettings.ShortIDs[0],
	}, nil
}

func (c *Client) doJSONWithRetry(ctx context.Context, path string, body []byte) error {
	resp, err := c.sendJSON(ctx, path, body)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		if err := c.LoginContext(ctx); err != nil {
			return fmt.Errorf("refresh panel session: %w", err)
		}
		resp, err = c.sendJSON(ctx, path, body)
		if err != nil {
			return err
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return responseError("panel request", resp)
	}
	return nil
}

func (c *Client) sendJSON(ctx context.Context, path string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrfToken)
	return c.httpClient.Do(req)
}

func (c *Client) sendGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-CSRF-Token", c.csrfToken)
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
