package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"vpn-bot/internal/service"
)

type WebhookServer struct {
	payService *service.PaymentService
	addr       string
	server     *http.Server
}

// NewWebhookServer accepts either a port ("8080") or an HTTP address (":8080").
func NewWebhookServer(port string, payService *service.PaymentService) *WebhookServer {
	if len(port) > 0 && port[0] != ':' {
		port = ":" + port
	}
	return &WebhookServer{payService: payService, addr: port}
}

func (ws *WebhookServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook/cryptobot", ws.handleCryptoWebhook)
	ws.server = &http.Server{Addr: ws.addr, Handler: mux}
	return ws.server.ListenAndServe()
}

func (ws *WebhookServer) Shutdown(ctx context.Context) error {
	if ws.server == nil {
		return nil
	}
	if err := ws.server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("shutdown webhook server: %w", err)
	}
	return nil
}

func (ws *WebhookServer) handleCryptoWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	defer r.Body.Close()
	webhookData, err := service.ParseWebhookData(r.Body)
	if err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := ws.payService.ProcessWebhook(r.Context(), webhookData); err != nil {
		log.Printf("webhook: process Crypto Pay payload: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("OK"))
}
