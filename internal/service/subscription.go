package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/google/uuid"

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
	AddClientContext(
		context.Context,
		[]int,
		string,
		string,
		string,
		int64,
		int64,
	) error

	GetClientContext(
		context.Context,
		string,
	) (*panel.ClientInfo, error)

	UpdateClientContext(
		context.Context,
		string,
		int64,
		int64,
	) error

	SyncClientInboundsContext(
		context.Context,
		string,
		[]int,
	) error

	DeleteClientContext(
		context.Context,
		string,
	) error

	GetClientTrafficsContext(
		context.Context,
		string,
	) (*panel.ClientTraffic, error)

	GetAllInboundsContext(
		context.Context,
	) ([]panel.Inbound, error)
}

type SubscriptionService struct {
	subs         SubscriptionRepository
	servers      ServerRepository
	panel        PanelClient
	now          func() time.Time
	subPort      int
	trafficBytes int64
}

func NewSubscriptionService(
	subs SubscriptionRepository,
	servers ServerRepository,
	panelClient PanelClient,
	subPort int,
	trafficGB int64,
) *SubscriptionService {
	var trafficBytes int64

	if trafficGB > 0 {
		trafficBytes = trafficGB * 1024 * 1024 * 1024
	}

	return &SubscriptionService{
		subs:         subs,
		servers:      servers,
		panel:        panelClient,
		now:          time.Now,
		subPort:      subPort,
		trafficBytes: trafficBytes,
	}
}

// ============================================================
// CREATE / RENEW SUBSCRIPTION
// ============================================================

func (s *SubscriptionService) CreateSubscription(
	c context.Context,
	user int64,
	days int,
) (string, error) {
	if days <= 0 {
		return "", fmt.Errorf(
			"create subscription: duration must be positive",
		)
	}

	now := s.now().UTC()

	old, err := s.subs.GetLatestByUserID(
		c,
		user,
	)

	// ========================================================
	// EXISTING SUBSCRIPTION
	// ========================================================

	if err == nil && old.IsActive {
		base := old.ExpiresAt

		if base.Before(now) {
			base = now
		}

		newExpiry := base.AddDate(
			0,
			0,
			days,
		)

		inbounds, err := s.panel.GetAllInboundsContext(c)
		if err != nil {
			return "", fmt.Errorf(
				"renew subscription: get inbounds: %w",
				err,
			)
		}

		inboundIDs := activeInboundIDs(inbounds)

		if len(inboundIDs) == 0 {
			return "", fmt.Errorf(
				"renew subscription: no active inbounds",
			)
		}

		// Проверяем, существует ли клиент в панели.
		_, panelErr := s.panel.GetClientContext(
			c,
			old.ClientEmail,
		)

		if panelErr != nil {
			// В БД подписка есть, но клиента в панели нет.
			// Восстанавливаем его с теми же:
			// email / UUID / sub_id.
			if err := s.panel.AddClientContext(
				c,
				inboundIDs,
				old.ClientEmail,
				old.PanelClientID,
				old.SubID,
				newExpiry.UnixMilli(),
				s.trafficBytes,
			); err != nil {
				return "", fmt.Errorf(
					"renew subscription: recreate missing panel client: %w",
					err,
				)
			}
		} else {
			if err := s.panel.UpdateClientContext(
				c,
				old.ClientEmail,
				newExpiry.UnixMilli(),
				s.trafficBytes,
			); err != nil {
				return "", fmt.Errorf(
					"renew subscription: update panel client: %w",
					err,
				)
			}

			if err := s.panel.SyncClientInboundsContext(
				c,
				old.ClientEmail,
				inboundIDs,
			); err != nil {
				return "", fmt.Errorf(
					"renew subscription: sync inbounds: %w",
					err,
				)
			}
		}

		old.ExpiresAt = newExpiry
		old.IsActive = true

		if err := s.subs.Update(
			c,
			old,
		); err != nil {
			return "", fmt.Errorf(
				"renew subscription: update database: %w",
				err,
			)
		}

		return old.SubscriptionURL, nil
	}

	// ========================================================
	// NEW SUBSCRIPTION
	// ========================================================

	server, err := s.servers.GetLeastLoaded(c)
	if err != nil {
		return "", fmt.Errorf(
			"create subscription: get server: %w",
			err,
		)
	}

	inbounds, err := s.panel.GetAllInboundsContext(c)
	if err != nil {
		return "", fmt.Errorf(
			"create subscription: get inbounds: %w",
			err,
		)
	}

	inboundIDs := activeInboundIDs(inbounds)

	if len(inboundIDs) == 0 {
		return "", fmt.Errorf(
			"create subscription: no active inbounds",
		)
	}

	clientID := uuid.NewString()
	subID := uuid.NewString()

	email := fmt.Sprintf(
		"sub_%d_%d",
		user,
		now.UnixNano(),
	)

	expiry := now.AddDate(
		0,
		0,
		days,
	)

	subscriptionURL, err := BuildSubscriptionURL(
		server.PanelURL,
		s.subPort,
		subID,
	)
	if err != nil {
		return "", fmt.Errorf(
			"create subscription: build URL: %w",
			err,
		)
	}

	// ========================================================
	// CREATE ONE CLIENT ON ALL ACTIVE INBOUNDS
	// ========================================================

	if err := s.panel.AddClientContext(
		c,
		inboundIDs,
		email,
		clientID,
		subID,
		expiry.UnixMilli(),
		s.trafficBytes,
	); err != nil {
		return "", fmt.Errorf(
			"create subscription: add panel client: %w",
			err,
		)
	}

	sub := &model.Subscription{
		ID:              uuid.NewString(),
		UserTgID:        user,
		ServerID:        server.ID,
		ClientEmail:     email,
		PanelClientID:   clientID,
		SubID:           subID,
		SubscriptionURL: subscriptionURL,
		ExpiresAt:       expiry,
		IsActive:        true,
	}

	if err := s.subs.Create(
		c,
		sub,
	); err != nil {
		// DB не сохранилась — откатываем клиента в панели.
		_ = s.panel.DeleteClientContext(
			c,
			email,
		)

		return "", fmt.Errorf(
			"create subscription: save subscription: %w",
			err,
		)
	}

	return subscriptionURL, nil
}

// ============================================================
// GET ACTIVE SUBSCRIPTION
// ============================================================

func (s *SubscriptionService) GetActiveSubscription(
	c context.Context,
	user int64,
) (*model.Subscription, *panel.ClientTraffic, error) {
	subscriptions, err := s.subs.GetActiveByUserID(c, user)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"get active subscription: %w",
			err,
		)
	}

	if len(subscriptions) == 0 {
		return nil, nil, ErrNoActiveSubscription
	}

	sub := subscriptions[0]

	traffic, err := s.panel.GetClientTrafficsContext(
		c,
		sub.ClientEmail,
	)
	if err != nil {
		return nil, nil, fmt.Errorf(
			"get active subscription traffic: %w",
			err,
		)
	}

	if traffic.ExpiryTime > 0 {
		sub.ExpiresAt = time.UnixMilli(traffic.ExpiryTime)
	}

	return &sub, traffic, nil
}

// ============================================================
// DISABLE EXPIRED
// ============================================================

func (s *SubscriptionService) DisableExpiredSubscriptions(
	c context.Context,
) (int, error) {
	expired, err := s.subs.GetExpired(
		c,
		s.now(),
	)
	if err != nil {
		return 0, fmt.Errorf(
			"disable expired subscriptions: %w",
			err,
		)
	}

	count := 0

	for i := range expired {
		sub := &expired[i]

		if err := s.panel.DeleteClientContext(
			c,
			sub.ClientEmail,
		); err != nil {
			return count, fmt.Errorf(
				"disable expired subscriptions: delete panel client %s: %w",
				sub.ClientEmail,
				err,
			)
		}

		sub.IsActive = false

		if err := s.subs.Update(
			c,
			sub,
		); err != nil {
			return count, fmt.Errorf(
				"disable expired subscriptions: update database: %w",
				err,
			)
		}

		count++
	}

	return count, nil
}

// ============================================================
// ACTIVE COUNT
// ============================================================

func (s *SubscriptionService) GetActiveCount(
	c context.Context,
) (int64, error) {
	return s.subs.GetActiveCount(
		c,
		s.now(),
	)
}

// ============================================================
// HELPERS
// ============================================================

func activeInboundIDs(
	inbounds []panel.Inbound,
) []int {
	ids := make(
		[]int,
		0,
		len(inbounds),
	)

	for _, inbound := range inbounds {
		if inbound.Enable {
			ids = append(
				ids,
				inbound.ID,
			)
		}
	}

	return ids
}

func BuildSubscriptionURL(
	panelURL string,
	port int,
	subID string,
) (string, error) {
	u, err := url.Parse(panelURL)
	if err != nil {
		return "", fmt.Errorf(
			"parse panel URL: %w",
			err,
		)
	}

	if u.Hostname() == "" {
		return "", fmt.Errorf(
			"panel URL has no hostname",
		)
	}

	host := net.JoinHostPort(
		u.Hostname(),
		fmt.Sprint(port),
	)

	return fmt.Sprintf(
		"http://%s/sub/%s",
		host,
		url.PathEscape(subID),
	), nil
}
