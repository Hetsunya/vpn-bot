package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vpn-bot/internal/model"
)

type UserRepo interface {
	Create(context.Context, *model.User) error
	GetByTgID(context.Context, int64) (*model.User, error)
	Update(context.Context, *model.User) error
	Delete(context.Context, int64) error
	GetTotal(context.Context) (int64, error)
}

type userRepo struct{ db *pgxpool.Pool }

func NewUserRepo(db *pgxpool.Pool) UserRepo { return &userRepo{db: db} }

func (r *userRepo) Create(ctx context.Context, user *model.User) error {
	_, err := r.db.Exec(ctx, `INSERT INTO users (tg_id, username, created_at) VALUES ($1, $2, $3)`, user.TgID, user.Username, user.CreatedAt)
	if err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

func (r *userRepo) GetByTgID(ctx context.Context, tgID int64) (*model.User, error) {
	user := &model.User{}
	err := r.db.QueryRow(ctx, `SELECT tg_id, username, created_at FROM users WHERE tg_id = $1`, tgID).Scan(&user.TgID, &user.Username, &user.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return user, nil
}

func (r *userRepo) Update(ctx context.Context, user *model.User) error {
	result, err := r.db.Exec(ctx, `UPDATE users SET username = $1 WHERE tg_id = $2`, user.Username, user.TgID)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *userRepo) Delete(ctx context.Context, tgID int64) error {
	result, err := r.db.Exec(ctx, `DELETE FROM users WHERE tg_id = $1`, tgID)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *userRepo) GetTotal(ctx context.Context) (int64, error) {
	var total int64
	if err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return total, nil
}
