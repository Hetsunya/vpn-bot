package model

import (
	"time"

	"github.com/shopspring/decimal"
)

type User struct {
	TgID      int64     `db:"tg_id"`
	Username  string    `db:"username"`
	CreatedAt time.Time `db:"created_at"`
}

type Server struct {
	ID        int    `db:"id"`
	Name      string `db:"name"`
	PanelURL  string `db:"panel_url"`
	APISecret string `db:"api_secret"`
	IsActive  bool   `db:"is_active"`
}

type Subscription struct {
	ID          string    `db:"id"`
	UserTgID    int64     `db:"user_tg_id"`
	ServerID    int       `db:"server_id"`
	ClientEmail string    `db:"client_email"`
	VlessUUID   string    `db:"vless_uuid"`
	ExpiresAt   time.Time `db:"expires_at"`
	IsActive    bool      `db:"is_active"`
}

type Payment struct {
	ID        string          `db:"id"`
	UserTgID  int64           `db:"user_tg_id"`
	InvoiceID int64           `db:"invoice_id"`
	Amount    decimal.Decimal `db:"amount"`
	Asset     string          `db:"asset"`
	Status    string          `db:"status"`
	CreatedAt time.Time       `db:"created_at"`
	PaidAt    *time.Time      `db:"paid_at"`
}
