package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vpn-bot/internal/model"
)

type SubRepo interface {
	Create(context.Context, *model.Subscription) error
	GetByID(context.Context, string) (*model.Subscription, error)
	GetActiveByUserID(context.Context, int64) ([]model.Subscription, error)
	GetExpired(context.Context, time.Time) ([]model.Subscription, error)
	Update(context.Context, *model.Subscription) error
	Delete(context.Context, string) error
}

type subRepo struct{ db *pgxpool.Pool }

func NewSubRepo(db *pgxpool.Pool) SubRepo { return &subRepo{db: db} }

func (r *subRepo) Create(ctx context.Context, sub *model.Subscription) error {
	_, err := r.db.Exec(ctx, `INSERT INTO subscriptions (id, user_tg_id, server_id, client_email, vless_uuid, expires_at, is_active) VALUES ($1, $2, $3, $4, $5, $6, $7)`, sub.ID, sub.UserTgID, sub.ServerID, sub.ClientEmail, sub.VlessUUID, sub.ExpiresAt, sub.IsActive)
	if err != nil {
		return fmt.Errorf("create subscription: %w", err)
	}
	return nil
}

func (r *subRepo) GetByID(ctx context.Context, id string) (*model.Subscription, error) {
	sub := &model.Subscription{}
	err := r.db.QueryRow(ctx, `SELECT id, user_tg_id, server_id, client_email, vless_uuid, expires_at, is_active FROM subscriptions WHERE id = $1`, id).Scan(&sub.ID, &sub.UserTgID, &sub.ServerID, &sub.ClientEmail, &sub.VlessUUID, &sub.ExpiresAt, &sub.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get subscription: %w", err)
	}
	return sub, nil
}

func (r *subRepo) GetActiveByUserID(ctx context.Context, userTgID int64) ([]model.Subscription, error) {
	rows, err := r.db.Query(ctx, `SELECT id, user_tg_id, server_id, client_email, vless_uuid, expires_at, is_active FROM subscriptions WHERE user_tg_id = $1 AND is_active = TRUE AND expires_at > NOW() ORDER BY expires_at`, userTgID)
	if err != nil {
		return nil, fmt.Errorf("get active subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (r *subRepo) GetExpired(ctx context.Context, now time.Time) ([]model.Subscription, error) {
	rows, err := r.db.Query(ctx, `SELECT id, user_tg_id, server_id, client_email, vless_uuid, expires_at, is_active FROM subscriptions WHERE is_active = TRUE AND expires_at <= $1 ORDER BY expires_at`, now)
	if err != nil {
		return nil, fmt.Errorf("get expired subscriptions: %w", err)
	}
	defer rows.Close()
	return scanSubscriptions(rows)
}

func (r *subRepo) Update(ctx context.Context, sub *model.Subscription) error {
	result, err := r.db.Exec(ctx, `UPDATE subscriptions SET user_tg_id = $1, server_id = $2, client_email = $3, vless_uuid = $4, expires_at = $5, is_active = $6 WHERE id = $7`, sub.UserTgID, sub.ServerID, sub.ClientEmail, sub.VlessUUID, sub.ExpiresAt, sub.IsActive, sub.ID)
	if err != nil {
		return fmt.Errorf("update subscription: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *subRepo) Delete(ctx context.Context, id string) error {
	result, err := r.db.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanSubscriptions(rows pgx.Rows) ([]model.Subscription, error) {
	subscriptions := make([]model.Subscription, 0)
	for rows.Next() {
		var sub model.Subscription
		if err := rows.Scan(&sub.ID, &sub.UserTgID, &sub.ServerID, &sub.ClientEmail, &sub.VlessUUID, &sub.ExpiresAt, &sub.IsActive); err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subscriptions = append(subscriptions, sub)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", err)
	}
	return subscriptions, nil
}
