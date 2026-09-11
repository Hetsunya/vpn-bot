package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"net"
	"net/url"
	"time"
	"vpn-bot/internal/client/panel"
	"vpn-bot/internal/model"
)

var ErrNoActiveSubscription = errors.New("no active subscription")

type SubscriptionRepository interface {
	Create(context.Context, *model.Subscription) error
	GetLatestByUserID(context.Context, int64) (*model.Subscription, error)
	GetActiveByUserID(context.Context, int64) ([]model.Subscription, error)
	GetExpired(context.Context, time.Time) ([]model.Subscription, error)
	Update(context.Context, *model.Subscription) error
	GetActiveCount(context.Context, time.Time) (int64, error)
}
type ServerRepository interface {
	GetByID(context.Context, int) (*model.Server, error)
	GetLeastLoaded(context.Context) (*model.Server, error)
}
type PanelClient interface {
	AddClientContext(context.Context, int, string, string, string, int64, int64) error
	DeleteClientContext(context.Context, int, string) error
	GetClientTrafficsContext(context.Context, string) (*panel.ClientTraffic, error)
}
type SubscriptionService struct {
	subs         SubscriptionRepository
	servers      ServerRepository
	panel        PanelClient
	now          func() time.Time
	subPort      int
	trafficBytes int64
}

func NewSubscriptionService(subs SubscriptionRepository, servers ServerRepository, p PanelClient, subPort int, trafficGB int64) *SubscriptionService {
	return &SubscriptionService{subs: subs, servers: servers, panel: p, now: time.Now, subPort: subPort, trafficBytes: trafficGB * 1024 * 1024 * 1024}
}
func (s *SubscriptionService) CreateSubscription(c context.Context, user int64, days int) (string, error) {
	if days <= 0 {
		return "", fmt.Errorf("create subscription: duration must be positive")
	}
	now := s.now().UTC()
	old, e := s.subs.GetLatestByUserID(c, user)
	if e == nil {
		base := old.ExpiresAt
		if base.Before(now) {
			base = now
		}
		old.ExpiresAt = base.AddDate(0, 0, days)
		old.IsActive = true
		if e = s.panel.AddClientContext(c, old.ServerID, old.ClientEmail, old.PanelClientID, old.SubID, old.ExpiresAt.UnixMilli(), s.trafficBytes); e != nil {
			return "", fmt.Errorf("renew subscription: sync panel client: %w", e)
		}
		if e = s.subs.Update(c, old); e != nil {
			return "", fmt.Errorf("renew subscription: update subscription: %w", e)
		}
		return old.SubscriptionURL, nil
	}
	server, e := s.servers.GetLeastLoaded(c)
	if e != nil {
		return "", fmt.Errorf("create subscription: get server: %w", e)
	}
	clientID := uuid.NewString()
	subID := uuid.NewString()
	email := fmt.Sprintf("sub_%d_%d", user, now.UnixNano())
	exp := now.AddDate(0, 0, days)
	link, e := BuildSubscriptionURL(server.PanelURL, s.subPort, subID)
	if e != nil {
		return "", fmt.Errorf("create subscription: build URL: %w", e)
	}
	if e = s.panel.AddClientContext(c, server.ID, email, clientID, subID, exp.UnixMilli(), s.trafficBytes); e != nil {
		return "", fmt.Errorf("create subscription: add panel client: %w", e)
	}
	sub := &model.Subscription{ID: uuid.NewString(), UserTgID: user, ServerID: server.ID, ClientEmail: email, PanelClientID: clientID, SubID: subID, SubscriptionURL: link, ExpiresAt: exp, IsActive: true}
	if e = s.subs.Create(c, sub); e != nil {
		_ = s.panel.DeleteClientContext(c, server.ID, email)
		return "", fmt.Errorf("create subscription: save subscription: %w", e)
	}
	return link, nil
}
func (s *SubscriptionService) GetActiveSubscription(c context.Context, user int64) (*model.Subscription, *panel.ClientTraffic, error) {
	a, e := s.subs.GetActiveByUserID(c, user)
	if e != nil {
		return nil, nil, fmt.Errorf("get active subscription: %w", e)
	}
	if len(a) == 0 {
		return nil, nil, ErrNoActiveSubscription
	}
	t, e := s.panel.GetClientTrafficsContext(c, a[0].ClientEmail)
	if e != nil {
		return nil, nil, fmt.Errorf("get active subscription traffic: %w", e)
	}
	if t.ExpiryTime > 0 {
		a[0].ExpiresAt = time.UnixMilli(t.ExpiryTime)
	}
	return &a[0], t, nil
}
func (s *SubscriptionService) DisableExpiredSubscriptions(c context.Context) (int, error) {
	a, e := s.subs.GetExpired(c, s.now())
	if e != nil {
		return 0, fmt.Errorf("disable expired subscriptions: %w", e)
	}
	n := 0
	for i := range a {
		if e = s.panel.DeleteClientContext(c, a[i].ServerID, a[i].ClientEmail); e != nil {
			return n, fmt.Errorf("disable expired subscriptions: %w", e)
		}
		a[i].IsActive = false
		if e = s.subs.Update(c, &a[i]); e != nil {
			return n, fmt.Errorf("disable expired subscriptions: %w", e)
		}
		n++
	}
	return n, nil
}
func (s *SubscriptionService) GetActiveCount(c context.Context) (int64, error) {
	return s.subs.GetActiveCount(c, s.now())
}
func BuildSubscriptionURL(panelURL string, port int, subID string) (string, error) {
	u, e := url.Parse(panelURL)
	if e != nil {
		return "", e
	}
	if u.Hostname() == "" || subID == "" {
		return "", fmt.Errorf("panel host or SubID missing")
	}
	return "http://" + net.JoinHostPort(u.Hostname(), fmt.Sprint(port)) + "/sub/" + url.PathEscape(subID), nil
}
