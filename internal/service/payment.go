package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"vpn-bot/internal/model"
)

const defaultPaymentAsset = "USDT"

type PaymentRepository interface {
	Create(context.Context, *model.Payment) error
	GetByInvoiceID(context.Context, int64) (*model.Payment, error)
	Update(context.Context, *model.Payment) error
}

type CryptoClient interface {
	CreateInvoiceContext(context.Context, string, string, string, string) (int64, string, error)
}

type SubscriptionCreator interface {
	CreateSubscription(context.Context, int64, int) (string, error)
}

type PaymentService struct {
	payments      PaymentRepository
	subscriptions SubscriptionCreator
	crypto        CryptoClient
	now           func() time.Time
}

func NewPaymentService(payments PaymentRepository, subscriptions SubscriptionCreator, cryptoClient CryptoClient) *PaymentService {
	return &PaymentService{payments: payments, subscriptions: subscriptions, crypto: cryptoClient, now: time.Now}
}

func (s *PaymentService) CreateInvoice(ctx context.Context, userTgID int64, amount string, durationDays int) (string, error) {
	if durationDays <= 0 {
		return "", fmt.Errorf("create invoice: duration must be positive")
	}
	decimalAmount, err := decimal.NewFromString(amount)
	if err != nil || !decimalAmount.IsPositive() {
		if err != nil {
			return "", fmt.Errorf("create invoice: parse amount: %w", err)
		}
		return "", fmt.Errorf("create invoice: amount must be positive")
	}
	payload, err := json.Marshal(struct {
		TgID         int64 `json:"tg_id"`
		DurationDays int   `json:"duration_days"`
	}{userTgID, durationDays})
	if err != nil {
		return "", fmt.Errorf("create invoice: marshal payload: %w", err)
	}

	invoiceID, payURL, err := s.crypto.CreateInvoiceContext(ctx, defaultPaymentAsset, amount, "VPN Subscription", string(payload))
	if err != nil {
		return "", fmt.Errorf("create invoice: create crypto invoice: %w", err)
	}
	payment := &model.Payment{ID: uuid.NewString(), UserTgID: userTgID, InvoiceID: invoiceID, Amount: decimalAmount, Asset: defaultPaymentAsset, Status: "active", CreatedAt: s.now().UTC()}
	if err := s.payments.Create(ctx, payment); err != nil {
		return "", fmt.Errorf("create invoice: save payment: %w", err)
	}
	return payURL, nil
}

// WebhookData is the normalized invoice data received from Crypto Pay.
// Its UnmarshalJSON accepts both a direct invoice object and Crypto Pay's envelope.
type WebhookData struct {
	InvoiceID int64  `json:"invoice_id"`
	Status    string `json:"status"`
	Payload   string `json:"payload"`
}

func ParseWebhookData(reader io.Reader) (WebhookData, error) {
	var data WebhookData
	if err := json.NewDecoder(reader).Decode(&data); err != nil {
		return WebhookData{}, fmt.Errorf("parse webhook data: %w", err)
	}
	return data, nil
}

func (w *WebhookData) UnmarshalJSON(data []byte) error {
	var direct struct {
		InvoiceID int64  `json:"invoice_id"`
		Status    string `json:"status"`
		Payload   string `json:"payload"`
	}
	if err := json.Unmarshal(data, &direct); err == nil && direct.InvoiceID != 0 {
		w.InvoiceID, w.Status, w.Payload = direct.InvoiceID, direct.Status, direct.Payload
		return nil
	}
	var envelope struct {
		Payload struct {
			InvoiceID int64  `json:"invoice_id"`
			Status    string `json:"status"`
			Payload   string `json:"payload"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	w.InvoiceID, w.Status, w.Payload = envelope.Payload.InvoiceID, envelope.Payload.Status, envelope.Payload.Payload
	return nil
}

func (s *PaymentService) ProcessWebhook(ctx context.Context, webhookPayload WebhookData) error {
	log.Printf(
		"crypto webhook: invoice=%d status=%s payload=%s",
		webhookPayload.InvoiceID,
		webhookPayload.Status,
		webhookPayload.Payload,
	)
	if webhookPayload.Status != "paid" {
		return fmt.Errorf("process webhook: unsupported invoice status %q", webhookPayload.Status)
	}
	payment, err := s.payments.GetByInvoiceID(ctx, webhookPayload.InvoiceID)
	if err != nil {
		return fmt.Errorf("process webhook: get payment: %w", err)
	}
	if payment.Status == "paid" {
		return nil
	}

	var payload struct {
		TgID         int64 `json:"tg_id"`
		DurationDays int   `json:"duration_days"`
	}
	if err := json.Unmarshal([]byte(webhookPayload.Payload), &payload); err != nil {
		return fmt.Errorf("process webhook: parse invoice payload: %w", err)
	}
	if payload.TgID == 0 || payload.DurationDays <= 0 {
		return fmt.Errorf("process webhook: invalid invoice payload")
	}
	if _, err := s.subscriptions.CreateSubscription(ctx, payload.TgID, payload.DurationDays); err != nil {
		log.Printf(
			"crypto webhook: subscription activation failed: %v",
			err,
		)
		return fmt.Errorf("process webhook: create subscription: %w", err)
	}
	log.Printf(
		"crypto webhook: subscription activated for user=%d",
		payload.TgID,
	)
	now := s.now().UTC()
	payment.Status, payment.PaidAt = "paid", &now
	if err := s.payments.Update(ctx, payment); err != nil {
		return fmt.Errorf("process webhook: update payment: %w", err)
	}
	return nil
}
