package repository

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"vpn-bot/internal/model"
)

type ServerRepo interface {
	Create(context.Context, *model.Server) error
	GetByID(context.Context, int) (*model.Server, error)
	GetAll(context.Context) ([]model.Server, error)
	Update(context.Context, *model.Server) error
	Delete(context.Context, int) error
}

type serverRepo struct{ db *pgxpool.Pool }

func NewServerRepo(db *pgxpool.Pool) ServerRepo { return &serverRepo{db: db} }

func (r *serverRepo) Create(ctx context.Context, server *model.Server) error {
	err := r.db.QueryRow(ctx, `INSERT INTO servers (name, panel_url, api_secret, is_active) VALUES ($1, $2, $3, $4) RETURNING id`, server.Name, server.PanelURL, server.APISecret, server.IsActive).Scan(&server.ID)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}
	return nil
}

func (r *serverRepo) GetByID(ctx context.Context, id int) (*model.Server, error) {
	server := &model.Server{}
	err := r.db.QueryRow(ctx, `SELECT id, name, panel_url, api_secret, is_active FROM servers WHERE id = $1`, id).Scan(&server.ID, &server.Name, &server.PanelURL, &server.APISecret, &server.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get server: %w", err)
	}
	return server, nil
}

func (r *serverRepo) GetAll(ctx context.Context) ([]model.Server, error) {
	rows, err := r.db.Query(ctx, `SELECT id, name, panel_url, api_secret, is_active FROM servers ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("get servers: %w", err)
	}
	defer rows.Close()
	servers := make([]model.Server, 0)
	for rows.Next() {
		var server model.Server
		if err := rows.Scan(&server.ID, &server.Name, &server.PanelURL, &server.APISecret, &server.IsActive); err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate servers: %w", err)
	}
	return servers, nil
}

func (r *serverRepo) Update(ctx context.Context, server *model.Server) error {
	result, err := r.db.Exec(ctx, `UPDATE servers SET name = $1, panel_url = $2, api_secret = $3, is_active = $4 WHERE id = $5`, server.Name, server.PanelURL, server.APISecret, server.IsActive, server.ID)
	if err != nil {
		return fmt.Errorf("update server: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *serverRepo) Delete(ctx context.Context, id int) error {
	result, err := r.db.Exec(ctx, `DELETE FROM servers WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete server: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
