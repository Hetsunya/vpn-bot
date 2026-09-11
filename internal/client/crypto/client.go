// Package crypto implements the Crypto Pay HTTP API client.
package crypto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	apiToken   string
	httpClient *http.Client
}

// Invoice is the part of a Crypto Pay invoice needed by the application.
type Invoice struct {
	InvoiceID int64  `json:"invoice_id"`
	Status    string `json:"status"`
	Asset     string `json:"asset"`
	Amount    string `json:"amount"`
	Payload   string `json:"payload"`
	PayURL    string `json:"bot_invoice_url"`
}

func NewClient(baseURL, apiToken string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), apiToken: apiToken, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) CreateInvoice(asset, amount, description, payload string) (int64, string, error) {
	return c.CreateInvoiceContext(context.Background(), asset, amount, description, payload)
}

func (c *Client) CreateInvoiceContext(ctx context.Context, asset, amount, description, payload string) (int64, string, error) {
	body, err := json.Marshal(struct {
		Asset       string `json:"asset"`
		Amount      string `json:"amount"`
		Description string `json:"description"`
		Payload     string `json:"payload"`
	}{asset, amount, description, payload})
	if err != nil {
		return 0, "", fmt.Errorf("marshal create invoice request: %w", err)
	}

	var response struct {
		OK        bool    `json:"ok"`
		Result    Invoice `json:"result"`
		Error     string  `json:"error"`
		ErrorCode int     `json:"error_code"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/createInvoice", body, &response); err != nil {
		return 0, "", fmt.Errorf("create invoice: %w", err)
	}
	if !response.OK {
		return 0, "", fmt.Errorf("create invoice: API error %d: %s", response.ErrorCode, response.Error)
	}
	log.Printf("crypto: created invoice %d", response.Result.InvoiceID)
	return response.Result.InvoiceID, response.Result.PayURL, nil
}

func (c *Client) GetInvoice(invoiceID int64) (*Invoice, error) {
	return c.GetInvoiceContext(context.Background(), invoiceID)
}

func (c *Client) GetInvoiceContext(ctx context.Context, invoiceID int64) (*Invoice, error) {
	path := "/getInvoices?invoice_ids=" + url.QueryEscape(strconv.FormatInt(invoiceID, 10))
	var response struct {
		OK        bool            `json:"ok"`
		Result    json.RawMessage `json:"result"`
		Error     string          `json:"error"`
		ErrorCode int             `json:"error_code"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, fmt.Errorf("get invoice: %w", err)
	}
	if !response.OK {
		return nil, fmt.Errorf("get invoice: API error %d: %s", response.ErrorCode, response.Error)
	}

	items, err := decodeInvoiceItems(response.Result)
	if err != nil {
		return nil, fmt.Errorf("decode invoices: %w", err)
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("get invoice: invoice %d not found", invoiceID)
	}
	return &items[0], nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, body []byte, destination any) error {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Crypto-Pay-API-Token", c.apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return cryptoResponseError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(destination); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func decodeInvoiceItems(raw json.RawMessage) ([]Invoice, error) {
	var items []Invoice
	if err := json.Unmarshal(raw, &items); err == nil {
		return items, nil
	}
	var result struct {
		Items []Invoice `json:"items"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return result.Items, nil
}

func cryptoResponseError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("unexpected HTTP status %s: %s", resp.Status, strings.TrimSpace(string(body)))
}
