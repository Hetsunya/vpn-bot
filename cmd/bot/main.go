package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"vpn-bot/internal/bot"
	"vpn-bot/internal/client/crypto"
	"vpn-bot/internal/client/panel"
	"vpn-bot/internal/config"
	"vpn-bot/internal/repository"
	"vpn-bot/internal/service"
	"vpn-bot/internal/worker"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	pool, err := repository.NewPostgres(appCtx, cfg.DatabaseDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	userRepo := repository.NewUserRepo(pool)
	serverRepo := repository.NewServerRepo(pool)
	subRepo := repository.NewSubRepo(pool)
	paymentRepo := repository.NewPaymentRepo(pool)
	panelClient := panel.NewClient(cfg.PanelURL, cfg.PanelUsername, cfg.PanelPassword)
	cryptoClient := crypto.NewClient(cfg.CryptoAPIURL, cfg.CryptoAPIToken)
	subService := service.NewSubscriptionService(subRepo, serverRepo, panelClient, cfg.PanelSubPort, cfg.DefaultSubTrafficGB)
	paymentService := service.NewPaymentService(paymentRepo, subService, cryptoClient)
	expiredWorker := worker.NewExpiredWorker(subService, time.Hour)
	telegramBot, err := bot.NewBot(cfg.BOTtoken, subService, paymentService, userRepo, serverRepo, cfg.AdminIDs, cfg.DefaultSubPrice, cfg.DefaultSubDurationDays)
	if err != nil {
		log.Fatal(err)
	}
	paymentService.SetPaymentSuccessHandler(
		telegramBot.SendMySubscription,
	)
	webhookServer := bot.NewWebhookServer(cfg.WebhookAddr, paymentService)
	go expiredWorker.Start(appCtx)

	go func() {
		if err := webhookServer.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("webhook server: %v", err)
		}
	}()
	go telegramBot.Start()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cancelApp()
	expiredWorker.Stop()
	telegramBot.Stop()
	if err := webhookServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown webhook server: %v", err)
	}
	log.Println("shutdown complete")
}
