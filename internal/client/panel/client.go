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
	"net/url"
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

type ClientTraffic struct {
	Up         int64
	Down       int64
	Total      int64
	ExpiryTime int64
}

type Inbound struct {
	ID     int    `json:"id"`
	Remark string `json:"remark"`
	Enable bool   `json:"enable"`
}

type ClientInfo struct {
	Client     map[string]interface{}
	InboundIDs []int
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
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+"/",
		nil,
	)
	if err != nil {
		return fmt.Errorf("create get request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("get request: %w", err)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	re := regexp.MustCompile(
		`<meta\s+name="csrf-token"\s+content="([^"]+)"`,
	)

	matches := re.FindSubmatch(bodyBytes)
	if len(matches) < 2 {
		return fmt.Errorf("csrf token not found in panel response")
	}

	c.csrfToken = string(matches[1])

	formData := strings.NewReader(
		fmt.Sprintf(
			"username=%s&password=%s",
			c.username,
			c.password,
		),
	)

	req, err = http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/login",
		formData,
	)
	if err != nil {
		return fmt.Errorf("create login request: %w", err)
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded",
	)
	req.Header.Set("X-CSRF-Token", c.csrfToken)

	resp, err = c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("login request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)

		return fmt.Errorf(
			"login: unexpected HTTP status %s, body: %s",
			resp.Status,
			string(body),
		)
	}

	var result struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf(
			"decode login response: %w",
			err,
		)
	}

	if !result.Success {
		return fmt.Errorf(
			"login failed: %s",
			result.Msg,
		)
	}

	log.Println("panel: authenticated successfully")

	return nil
}

func (c *Client) ensureAuth(ctx context.Context) error {
	c.mu.RLock()
	hasSession := c.csrfToken != ""
	c.mu.RUnlock()

	if !hasSession {
		return c.LoginContext(ctx)
	}

	return nil
}

// ============================================================
// CREATE CLIENT
// ============================================================

// AddClient создаёт ОДНОГО клиента и сразу привязывает его
// ко всем переданным inbound'ам.
func (c *Client) AddClient(
	inboundIDs []int,
	email string,
	clientUUID string,
	subID string,
	expiryTimeMs int64,
	totalGB int64,
) error {
	return c.AddClientContext(
		context.Background(),
		inboundIDs,
		email,
		clientUUID,
		subID,
		expiryTimeMs,
		totalGB,
	)
}

func (c *Client) AddClientContext(
	ctx context.Context,
	inboundIDs []int,
	email string,
	clientUUID string,
	subID string,
	expiryTimeMs int64,
	totalGB int64,
) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf(
			"authenticate add client request: %w",
			err,
		)
	}

	if len(inboundIDs) == 0 {
		return fmt.Errorf(
			"add client: no inbound IDs provided",
		)
	}

	client := map[string]interface{}{
		"email":      email,
		"id":         clientUUID,
		"subId":      subID,
		"expiryTime": expiryTimeMs,
		"totalGB":    totalGB,
		"enable":     true,
	}

	body, err := json.Marshal(map[string]interface{}{
		"client":     client,
		"inboundIds": inboundIDs,
	})
	if err != nil {
		return fmt.Errorf(
			"encode add client request: %w",
			err,
		)
	}

	if err := c.doJSONWithRetry(
		ctx,
		"/panel/api/clients/add",
		body,
	); err != nil {
		return fmt.Errorf(
			"add client: %w",
			err,
		)
	}

	return nil
}

// ============================================================
// GET CLIENT
// ============================================================

func (c *Client) GetClientContext(
	ctx context.Context,
	email string,
) (*ClientInfo, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf(
			"authenticate get client request: %w",
			err,
		)
	}

	path := fmt.Sprintf(
		"/panel/api/clients/get/%s",
		url.PathEscape(email),
	)

	resp, err := c.sendGet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf(
			"get client: %w",
			err,
		)
	}

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {

		resp.Body.Close()

		if err := c.LoginContext(ctx); err != nil {
			return nil, fmt.Errorf(
				"refresh panel session: %w",
				err,
			)
		}

		resp, err = c.sendGet(ctx, path)
		if err != nil {
			return nil, fmt.Errorf(
				"get client after re-login: %w",
				err,
			)
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return nil, responseError(
			"get client",
			resp,
		)
	}

	var response struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`

		Obj struct {
			Client     map[string]interface{} `json:"client"`
			InboundIDs []int                  `json:"inboundIds"`
		} `json:"obj"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf(
			"decode get client response: %w",
			err,
		)
	}

	if !response.Success {
		return nil, fmt.Errorf(
			"get client: %s",
			response.Msg,
		)
	}

	return &ClientInfo{
		Client:     response.Obj.Client,
		InboundIDs: response.Obj.InboundIDs,
	}, nil
}

// ============================================================
// UPDATE CLIENT
// ============================================================

func (c *Client) UpdateClientContext(
	ctx context.Context,
	email string,
	expiryTimeMs int64,
	totalGB int64,
) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf(
			"authenticate update client request: %w",
			err,
		)
	}

	info, err := c.GetClientContext(
		ctx,
		email,
	)
	if err != nil {
		return fmt.Errorf(
			"get existing client before update: %w",
			err,
		)
	}

	info.Client["expiryTime"] = expiryTimeMs
	info.Client["totalGB"] = totalGB
	info.Client["enable"] = true

	body, err := json.Marshal(info.Client)
	if err != nil {
		return fmt.Errorf(
			"encode update client request: %w",
			err,
		)
	}

	path := fmt.Sprintf(
		"/panel/api/clients/update/%s",
		url.PathEscape(email),
	)

	if err := c.doJSONWithRetry(
		ctx,
		path,
		body,
	); err != nil {
		return fmt.Errorf(
			"update client: %w",
			err,
		)
	}

	return nil
}

// ============================================================
// SYNC INBOUNDS
// ============================================================

func (c *Client) SyncClientInboundsContext(
	ctx context.Context,
	email string,
	requiredInboundIDs []int,
) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf(
			"authenticate sync client request: %w",
			err,
		)
	}

	info, err := c.GetClientContext(
		ctx,
		email,
	)
	if err != nil {
		return fmt.Errorf(
			"get client for inbound sync: %w",
			err,
		)
	}

	current := make(map[int]bool)

	for _, id := range info.InboundIDs {
		current[id] = true
	}

	required := make(map[int]bool)

	for _, id := range requiredInboundIDs {
		required[id] = true
	}

	var attach []int

	for id := range required {
		if !current[id] {
			attach = append(
				attach,
				id,
			)
		}
	}

	var detach []int

	for id := range current {
		if !required[id] {
			detach = append(
				detach,
				id,
			)
		}
	}

	if len(attach) > 0 {
		body, err := json.Marshal(
			map[string]interface{}{
				"inboundIds": attach,
			},
		)
		if err != nil {
			return fmt.Errorf(
				"encode attach inbounds request: %w",
				err,
			)
		}

		path := fmt.Sprintf(
			"/panel/api/clients/%s/attach",
			url.PathEscape(email),
		)

		if err := c.doJSONWithRetry(
			ctx,
			path,
			body,
		); err != nil {
			return fmt.Errorf(
				"attach inbounds: %w",
				err,
			)
		}
	}

	if len(detach) > 0 {
		body, err := json.Marshal(
			map[string]interface{}{
				"inboundIds": detach,
			},
		)
		if err != nil {
			return fmt.Errorf(
				"encode detach inbounds request: %w",
				err,
			)
		}

		path := fmt.Sprintf(
			"/panel/api/clients/%s/detach",
			url.PathEscape(email),
		)

		if err := c.doJSONWithRetry(
			ctx,
			path,
			body,
		); err != nil {
			return fmt.Errorf(
				"detach inbounds: %w",
				err,
			)
		}
	}

	return nil
}

// ============================================================
// DELETE CLIENT
// ============================================================

func (c *Client) DeleteClient(email string) error {
	return c.DeleteClientContext(
		context.Background(),
		email,
	)
}

func (c *Client) DeleteClientContext(
	ctx context.Context,
	email string,
) error {
	if err := c.ensureAuth(ctx); err != nil {
		return fmt.Errorf(
			"authenticate delete client request: %w",
			err,
		)
	}

	path := fmt.Sprintf(
		"/panel/api/clients/del/%s",
		url.PathEscape(email),
	)

	if err := c.doJSONWithRetry(
		ctx,
		path,
		nil,
	); err != nil {
		return fmt.Errorf(
			"delete client: %w",
			err,
		)
	}

	return nil
}

// ============================================================
// TRAFFIC
// ============================================================

func (c *Client) GetClientTraffics(
	email string,
) (*ClientTraffic, error) {
	return c.GetClientTrafficsContext(
		context.Background(),
		email,
	)
}

func (c *Client) GetClientTrafficsContext(
	ctx context.Context,
	email string,
) (*ClientTraffic, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf(
			"authenticate get client traffics request: %w",
			err,
		)
	}

	path := fmt.Sprintf(
		"/panel/api/clients/traffic/%s",
		url.PathEscape(email),
	)

	resp, err := c.sendGet(
		ctx,
		path,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"get client traffics: %w",
			err,
		)
	}

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {

		resp.Body.Close()

		if err := c.LoginContext(ctx); err != nil {
			return nil, fmt.Errorf(
				"refresh panel session: %w",
				err,
			)
		}

		resp, err = c.sendGet(
			ctx,
			path,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"get client traffics after re-login: %w",
				err,
			)
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return nil, responseError(
			"get client traffics",
			resp,
		)
	}

	var response struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`

		Obj struct {
			Up         int64 `json:"up"`
			Down       int64 `json:"down"`
			Total      int64 `json:"total"`
			ExpiryTime int64 `json:"expiryTime"`
		} `json:"obj"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf(
			"decode client traffics response: %w",
			err,
		)
	}

	if !response.Success {
		return nil, fmt.Errorf(
			"get client traffics: %s",
			response.Msg,
		)
	}

	return &ClientTraffic{
		Up:         response.Obj.Up,
		Down:       response.Obj.Down,
		Total:      response.Obj.Total,
		ExpiryTime: response.Obj.ExpiryTime,
	}, nil
}

// ============================================================
// INBOUNDS
// ============================================================

func (c *Client) GetAllInbounds() ([]Inbound, error) {
	return c.GetAllInboundsContext(
		context.Background(),
	)
}

func (c *Client) GetAllInboundsContext(
	ctx context.Context,
) ([]Inbound, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf(
			"authenticate get all inbounds request: %w",
			err,
		)
	}

	resp, err := c.sendGet(
		ctx,
		"/panel/api/inbounds/list",
	)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {

		resp.Body.Close()

		if err := c.LoginContext(ctx); err != nil {
			return nil, fmt.Errorf(
				"refresh panel session: %w",
				err,
			)
		}

		resp, err = c.sendGet(
			ctx,
			"/panel/api/inbounds/list",
		)
		if err != nil {
			return nil, err
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return nil, responseError(
			"get all inbounds",
			resp,
		)
	}

	var response struct {
		Success bool      `json:"success"`
		Msg     string    `json:"msg"`
		Obj     []Inbound `json:"obj"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf(
			"decode inbounds response: %w",
			err,
		)
	}

	if !response.Success {
		return nil, fmt.Errorf(
			"get all inbounds: %s",
			response.Msg,
		)
	}

	return response.Obj, nil
}

// ============================================================
// LEGACY INBOUND SETTINGS
// ============================================================

func (c *Client) GetInboundSettings(
	inboundID int,
) (*InboundSettings, error) {
	return c.GetInboundSettingsContext(
		context.Background(),
		inboundID,
	)
}

func (c *Client) GetInboundSettingsContext(
	ctx context.Context,
	inboundID int,
) (*InboundSettings, error) {
	if err := c.ensureAuth(ctx); err != nil {
		return nil, fmt.Errorf(
			"authenticate get inbound settings request: %w",
			err,
		)
	}

	resp, err := c.sendGet(
		ctx,
		fmt.Sprintf(
			"/panel/api/inbounds/get/%d",
			inboundID,
		),
	)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {

		resp.Body.Close()

		if err := c.LoginContext(ctx); err != nil {
			return nil, fmt.Errorf(
				"refresh panel session: %w",
				err,
			)
		}

		resp, err = c.sendGet(
			ctx,
			fmt.Sprintf(
				"/panel/api/inbounds/get/%d",
				inboundID,
			),
		)
		if err != nil {
			return nil, err
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return nil, responseError(
			"get inbound settings",
			resp,
		)
	}

	var response struct {
		Success bool   `json:"success"`
		Msg     string `json:"msg"`

		Obj struct {
			Port           int             `json:"port"`
			StreamSettings json.RawMessage `json:"streamSettings"`
		} `json:"obj"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf(
			"decode inbound settings response: %w",
			err,
		)
	}

	if !response.Success {
		return nil, fmt.Errorf(
			"get inbound settings: %s",
			response.Msg,
		)
	}

	var stream struct {
		RealitySettings struct {
			PublicKey   string   `json:"publicKey"`
			ServerNames []string `json:"serverNames"`
			ShortIDs    []string `json:"shortIds"`
		} `json:"realitySettings"`
	}

	if err := json.Unmarshal(
		response.Obj.StreamSettings,
		&stream,
	); err != nil {
		return nil, fmt.Errorf(
			"decode reality settings: %w",
			err,
		)
	}

	if len(stream.RealitySettings.ServerNames) == 0 ||
		len(stream.RealitySettings.ShortIDs) == 0 ||
		stream.RealitySettings.PublicKey == "" {

		return nil, fmt.Errorf(
			"incomplete Reality settings",
		)
	}

	host := strings.TrimPrefix(
		c.baseURL,
		"http://",
	)

	host = strings.TrimPrefix(
		host,
		"https://",
	)

	host = strings.Split(
		host,
		":",
	)[0]

	return &InboundSettings{
		ServerIP:   host,
		ServerPort: response.Obj.Port,
		PublicKey:  stream.RealitySettings.PublicKey,
		SNI:        stream.RealitySettings.ServerNames[0],
		ShortID:    stream.RealitySettings.ShortIDs[0],
	}, nil
}

// ============================================================
// HTTP HELPERS
// ============================================================

func (c *Client) doJSONWithRetry(
	ctx context.Context,
	path string,
	body []byte,
) error {
	resp, err := c.sendJSON(
		ctx,
		path,
		body,
	)
	if err != nil {
		return err
	}

	if resp.StatusCode == http.StatusUnauthorized ||
		resp.StatusCode == http.StatusForbidden {

		resp.Body.Close()

		if err := c.LoginContext(ctx); err != nil {
			return fmt.Errorf(
				"refresh panel session: %w",
				err,
			)
		}

		resp, err = c.sendJSON(
			ctx,
			path,
			body,
		)
		if err != nil {
			return err
		}
	}

	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK ||
		resp.StatusCode >= http.StatusMultipleChoices {

		return responseError(
			"panel request",
			resp,
		)
	}

	return nil
}

func (c *Client) sendJSON(
	ctx context.Context,
	path string,
	body []byte,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+path,
		bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create request: %w",
			err,
		)
	}

	req.Header.Set(
		"Content-Type",
		"application/json",
	)

	req.Header.Set(
		"X-CSRF-Token",
		c.csrfToken,
	)

	return c.httpClient.Do(req)
}

func (c *Client) sendGet(
	ctx context.Context,
	path string,
) (*http.Response, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		c.baseURL+path,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"create request: %w",
			err,
		)
	}

	req.Header.Set(
		"X-CSRF-Token",
		c.csrfToken,
	)

	return c.httpClient.Do(req)
}

func responseError(
	operation string,
	resp *http.Response,
) error {
	const maxBody = 4 << 10

	body, _ := io.ReadAll(
		io.LimitReader(
			resp.Body,
			maxBody,
		),
	)

	if len(body) == 0 {
		return fmt.Errorf(
			"%s: unexpected HTTP status %s",
			operation,
			resp.Status,
		)
	}

	return fmt.Errorf(
		"%s: unexpected HTTP status %s: %s",
		operation,
		resp.Status,
		strings.TrimSpace(string(body)),
	)
}
