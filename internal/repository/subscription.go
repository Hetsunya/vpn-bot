package repository

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
	"vpn-bot/internal/model"
)

type SubRepo interface {
	Create(context.Context, *model.Subscription) error
	GetByID(context.Context, string) (*model.Subscription, error)
	GetLatestByUserID(context.Context, int64) (*model.Subscription, error)
	GetByClientEmail(context.Context, string) (*model.Subscription, error)
	GetBySubID(context.Context, string) (*model.Subscription, error)
	GetActiveByUserID(context.Context, int64) ([]model.Subscription, error)
	GetExpired(context.Context, time.Time) ([]model.Subscription, error)
	Update(context.Context, *model.Subscription) error
	Delete(context.Context, string) error
	GetActiveCount(context.Context, time.Time) (int64, error)
}
type subRepo struct{ db *pgxpool.Pool }

func NewSubRepo(db *pgxpool.Pool) SubRepo { return &subRepo{db} }

const subscriptionColumns = `id,user_tg_id,server_id,client_email,panel_client_id,sub_id,subscription_url,expires_at,is_active`

func (r *subRepo) Create(c context.Context, s *model.Subscription) error {
	_, e := r.db.Exec(c, `INSERT INTO subscriptions (`+subscriptionColumns+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, s.ID, s.UserTgID, s.ServerID, s.ClientEmail, s.PanelClientID, s.SubID, s.SubscriptionURL, s.ExpiresAt, s.IsActive)
	if e != nil {
		return fmt.Errorf("create subscription: %w", e)
	}
	return nil
}
func (r *subRepo) GetByID(c context.Context, id string) (*model.Subscription, error) {
	return r.one(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE id=$1`, id)
}
func (r *subRepo) GetLatestByUserID(c context.Context, id int64) (*model.Subscription, error) {
	return r.one(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE user_tg_id=$1 ORDER BY is_active DESC,expires_at DESC LIMIT 1`, id)
}
func (r *subRepo) GetByClientEmail(c context.Context, v string) (*model.Subscription, error) {
	return r.one(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE client_email=$1`, v)
}
func (r *subRepo) GetBySubID(c context.Context, v string) (*model.Subscription, error) {
	return r.one(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE sub_id=$1`, v)
}
func (r *subRepo) one(c context.Context, q string, a any) (*model.Subscription, error) {
	s := &model.Subscription{}
	e := r.db.QueryRow(c, q, a).Scan(&s.ID, &s.UserTgID, &s.ServerID, &s.ClientEmail, &s.PanelClientID, &s.SubID, &s.SubscriptionURL, &s.ExpiresAt, &s.IsActive)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if e != nil {
		return nil, fmt.Errorf("get subscription: %w", e)
	}
	return s, nil
}
func (r *subRepo) GetActiveByUserID(c context.Context, id int64) ([]model.Subscription, error) {
	return r.many(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE user_tg_id=$1 AND is_active=TRUE AND expires_at>NOW() AND sub_id IS NOT NULL ORDER BY expires_at`, id)
}
func (r *subRepo) GetExpired(c context.Context, n time.Time) ([]model.Subscription, error) {
	return r.many(c, `SELECT `+subscriptionColumns+` FROM subscriptions WHERE is_active=TRUE AND expires_at<=$1 ORDER BY expires_at`, n)
}
func (r *subRepo) many(c context.Context, q string, a any) ([]model.Subscription, error) {
	rows, e := r.db.Query(c, q, a)
	if e != nil {
		return nil, fmt.Errorf("get subscriptions: %w", e)
	}
	defer rows.Close()
	out := []model.Subscription{}
	for rows.Next() {
		var s model.Subscription
		if e = rows.Scan(&s.ID, &s.UserTgID, &s.ServerID, &s.ClientEmail, &s.PanelClientID, &s.SubID, &s.SubscriptionURL, &s.ExpiresAt, &s.IsActive); e != nil {
			return nil, fmt.Errorf("scan subscription: %w", e)
		}
		out = append(out, s)
	}
	if e = rows.Err(); e != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", e)
	}
	return out, nil
}
func (r *subRepo) Update(c context.Context, s *model.Subscription) error {
	x, e := r.db.Exec(c, `UPDATE subscriptions SET user_tg_id=$1,server_id=$2,client_email=$3,panel_client_id=$4,sub_id=$5,subscription_url=$6,expires_at=$7,is_active=$8,updated_at=NOW() WHERE id=$9`, s.UserTgID, s.ServerID, s.ClientEmail, s.PanelClientID, s.SubID, s.SubscriptionURL, s.ExpiresAt, s.IsActive, s.ID)
	if e != nil {
		return fmt.Errorf("update subscription: %w", e)
	}
	if x.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (r *subRepo) Delete(c context.Context, id string) error {
	x, e := r.db.Exec(c, `DELETE FROM subscriptions WHERE id=$1`, id)
	if e != nil {
		return fmt.Errorf("delete subscription: %w", e)
	}
	if x.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
func (r *subRepo) GetActiveCount(c context.Context, n time.Time) (int64, error) {
	var x int64
	e := r.db.QueryRow(c, `SELECT COUNT(*) FROM subscriptions WHERE is_active=TRUE AND expires_at>$1 AND sub_id IS NOT NULL`, n).Scan(&x)
	if e != nil {
		return 0, fmt.Errorf("count active subscriptions: %w", e)
	}
	return x, nil
}
