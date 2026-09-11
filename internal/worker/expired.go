// Package worker contains background application jobs.
package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"vpn-bot/internal/service"
)

type ExpiredWorker struct {
	subService *service.SubscriptionService
	interval   time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
}

func NewExpiredWorker(subService *service.SubscriptionService, interval time.Duration) *ExpiredWorker {
	if interval <= 0 {
		interval = time.Hour
	}
	return &ExpiredWorker{subService: subService, interval: interval, stopCh: make(chan struct{})}
}

func (w *ExpiredWorker) Start(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	log.Printf("expired worker started with interval %v", w.interval)
	for {
		select {
		case <-ticker.C:
			count, err := w.subService.DisableExpiredSubscriptions(ctx)
			if err != nil {
				log.Printf("expired worker error: %v", err)
				continue
			}
			if count > 0 {
				log.Printf("expired worker: disabled %d expired subscriptions", count)
			}
		case <-w.stopCh:
			log.Println("expired worker stopped")
			return
		case <-ctx.Done():
			log.Println("expired worker stopped: context cancelled")
			return
		}
	}
}

// Stop is idempotent and never blocks, including if Start was not called.
func (w *ExpiredWorker) Stop() { w.stopOnce.Do(func() { close(w.stopCh) }) }
