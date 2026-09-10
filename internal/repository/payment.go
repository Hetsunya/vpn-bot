package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vpn-bot/internal/model"
)

type PaymentRepo interface {
	Create(context.Context, *model.Payment) error
	GetByID(context.Context, string) (*model.Payment, error)
	GetByInvoiceID(context.Context, int64) (*model.Payment, error)
	Update(context.Context, *model.Payment) error
	Delete(context.Context, string) error
}

type paymentRepo struct{ db *pgxpool.Pool }

func NewPaymentRepo(db *pgxpool.Pool) PaymentRepo { return &paymentRepo{db: db} }

func (r *paymentRepo) Create(ctx context.Context, payment *model.Payment) error {
	_, err := r.db.Exec(ctx, `INSERT INTO payments (id, user_tg_id, invoice_id, amount, asset, status, created_at, paid_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, payment.ID, payment.UserTgID, payment.InvoiceID, payment.Amount, payment.Asset, payment.Status, payment.CreatedAt, payment.PaidAt)
	if err != nil {
		return fmt.Errorf("create payment: %w", err)
	}
	return nil
}

func (r *paymentRepo) GetByID(ctx context.Context, id string) (*model.Payment, error) {
	payment := &model.Payment{}
	err := r.db.QueryRow(ctx, `SELECT id, user_tg_id, invoice_id, amount, asset, status, created_at, paid_at FROM payments WHERE id = $1`, id).Scan(&payment.ID, &payment.UserTgID, &payment.InvoiceID, &payment.Amount, &payment.Asset, &payment.Status, &payment.CreatedAt, &payment.PaidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get payment: %w", err)
	}
	return payment, nil
}

func (r *paymentRepo) GetByInvoiceID(ctx context.Context, invoiceID int64) (*model.Payment, error) {
	payment := &model.Payment{}
	err := r.db.QueryRow(ctx, `SELECT id, user_tg_id, invoice_id, amount, asset, status, created_at, paid_at FROM payments WHERE invoice_id = $1`, invoiceID).Scan(&payment.ID, &payment.UserTgID, &payment.InvoiceID, &payment.Amount, &payment.Asset, &payment.Status, &payment.CreatedAt, &payment.PaidAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get payment by invoice id: %w", err)
	}
	return payment, nil
}

func (r *paymentRepo) Update(ctx context.Context, payment *model.Payment) error {
	result, err := r.db.Exec(ctx, `UPDATE payments SET user_tg_id = $1, invoice_id = $2, amount = $3, asset = $4, status = $5, created_at = $6, paid_at = $7 WHERE id = $8`, payment.UserTgID, payment.InvoiceID, payment.Amount, payment.Asset, payment.Status, payment.CreatedAt, payment.PaidAt, payment.ID)
	if err != nil {
		return fmt.Errorf("update payment: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *paymentRepo) Delete(ctx context.Context, id string) error {
	result, err := r.db.Exec(ctx, `DELETE FROM payments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete payment: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
