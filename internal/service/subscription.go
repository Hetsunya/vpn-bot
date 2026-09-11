// Package service contains application business logic.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/google/uuid"

	"vpn-bot/internal/client/panel"
	"vpn-bot/internal/model"
)

var ErrNoActiveSubscription = errors.New("no active subscription")

type SubscriptionRepository interface {
	Create(context.Context, *model.Subscription) error
	GetActiveByUserID(context.Context, int64) ([]model.Subscription, error)
	GetExpired(context.Context, time.Time) ([]model.Subscription, error)
	Update(context.Context, *model.Subscription) error
	GetActiveCount(context.Context, time.Time) (int64, error)
}

type ServerRepository interface {
	GetByID(context.Context, int) (*model.Server, error)
	GetLeastLoaded(context.Context) (*model.Server, error)
}

// PanelClient deliberately contains context-aware methods so a caller can cancel a service operation.
type PanelClient interface {
	AddClientContext(context.Context, int, string, string, int64) error
	DeleteClientContext(context.Context, int, string) error
	GetInboundSettingsContext(context.Context, int) (*panel.InboundSettings, error)
}

type SubscriptionService struct {
	subs    SubscriptionRepository
	servers ServerRepository
	panel   PanelClient
	now     func() time.Time
}

func NewSubscriptionService(subs SubscriptionRepository, servers ServerRepository, panelClient PanelClient) *SubscriptionService {
	return &SubscriptionService{subs: subs, servers: servers, panel: panelClient, now: time.Now}
}

func (s *SubscriptionService) CreateSubscription(ctx context.Context, userTgID int64, durationDays int) (string, error) {
	if durationDays <= 0 {
		return "", fmt.Errorf("create subscription: duration must be positive")
	}
	server, err := s.servers.GetLeastLoaded(ctx)
	if err != nil {
		return "", fmt.Errorf("create subscription: get least loaded server: %w", err)
	}

	now := s.now().UTC()
	vlessUUID := uuid.NewString()
	email := fmt.Sprintf("user_%d_%d", userTgID, now.UnixNano())
	expiresAt := now.AddDate(0, 0, durationDays)
	if err := s.panel.AddClientContext(ctx, server.ID, email, vlessUUID, expiresAt.UnixMilli()); err != nil {
		return "", fmt.Errorf("create subscription: add panel client: %w", err)
	}

	sub := &model.Subscription{
		ID:          uuid.NewString(),
		UserTgID:    userTgID,
		ServerID:    server.ID,
		ClientEmail: email,
		VlessUUID:   vlessUUID,
		ExpiresAt:   expiresAt,
		IsActive:    true,
	}
	if err := s.subs.Create(ctx, sub); err != nil {
		// Compensate for a failed database write so the panel does not retain an orphan client.
		if deleteErr := s.panel.DeleteClientContext(ctx, server.ID, email); deleteErr != nil {
			return "", fmt.Errorf("create subscription: save subscription: %w (rollback panel client: %v)", err, deleteErr)
		}
		return "", fmt.Errorf("create subscription: save subscription: %w", err)
	}

	link, err := s.vlessLink(ctx, sub, server)
	if err != nil {
		return "", fmt.Errorf("create subscription: build VLESS link: %w", err)
	}
	return link, nil
}

func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, userTgID int64) (*model.Subscription, string, error) {
	subscriptions, err := s.subs.GetActiveByUserID(ctx, userTgID)
	if err != nil {
		return nil, "", fmt.Errorf("get active subscription: query subscriptions: %w", err)
	}
	if len(subscriptions) == 0 {
		return nil, "", ErrNoActiveSubscription
	}
	sub := subscriptions[0]
	server, err := s.servers.GetByID(ctx, sub.ServerID)
	if err != nil {
		return nil, "", fmt.Errorf("get active subscription: get server: %w", err)
	}
	link, err := s.vlessLink(ctx, &sub, server)
	if err != nil {
		return nil, "", fmt.Errorf("get active subscription: build VLESS link: %w", err)
	}
	return &sub, link, nil
}

func (s *SubscriptionService) DisableExpiredSubscriptions(ctx context.Context) (int, error) {
	subscriptions, err := s.subs.GetExpired(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("disable expired subscriptions: query expired subscriptions: %w", err)
	}
	disabled := 0
	for i := range subscriptions {
		sub := &subscriptions[i]
		if err := s.panel.DeleteClientContext(ctx, sub.ServerID, sub.ClientEmail); err != nil {
			return disabled, fmt.Errorf("disable expired subscriptions: delete panel client %s: %w", sub.ID, err)
		}
		sub.IsActive = false
		if err := s.subs.Update(ctx, sub); err != nil {
			return disabled, fmt.Errorf("disable expired subscriptions: update subscription %s: %w", sub.ID, err)
		}
		disabled++
	}
	return disabled, nil
}

func (s *SubscriptionService) GetActiveCount(ctx context.Context) (int64, error) {
	total, err := s.subs.GetActiveCount(ctx, s.now())
	if err != nil {
		return 0, fmt.Errorf("get active subscription count: %w", err)
	}
	return total, nil
}

func (s *SubscriptionService) vlessLink(ctx context.Context, sub *model.Subscription, server *model.Server) (string, error) {
	settings, err := s.panel.GetInboundSettingsContext(ctx, sub.ServerID)
	if err != nil {
		return "", fmt.Errorf("get inbound settings: %w", err)
	}
	if settings.ServerIP == "" || settings.ServerPort <= 0 || settings.PublicKey == "" || settings.SNI == "" || settings.ShortID == "" {
		return "", fmt.Errorf("inbound settings are incomplete")
	}
	query := url.Values{
		"type":       {"tcp"},
		"security":   {"reality"},
		"pbk":        {settings.PublicKey},
		"fp":         {"chrome"},
		"sni":        {settings.SNI},
		"sid":        {settings.ShortID},
		"headerType": {"none"},
	}
	return fmt.Sprintf("vless://%s@%s:%d?%s#%s", sub.VlessUUID, settings.ServerIP, settings.ServerPort, query.Encode(), url.QueryEscape(server.Name)), nil
}
