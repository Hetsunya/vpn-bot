package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	BOTtoken string
	BOTName  string
	AdminIDs []int64

	DefaultSubPrice        string
	DefaultSubDurationDays int
	DefaultSubTrafficGB    int64
	WebhookAddr            string
	PanelSubPort           int

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string

	PanelURL       string
	PanelUsername  string
	PanelPassword  string
	CryptoAPIURL   string
	CryptoAPIToken string
}

func Load() (*Config, error) {

	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("load .env: %w", err)
	}
	adminIDs, err := parseAdminIDs(os.Getenv("ADMIN_IDS"))
	if err != nil {
		return nil, err
	}

	config := Config{
		BOTtoken: os.Getenv("BOT_TOKEN"),
		BOTName:  os.Getenv("BOT_NAME"),
		AdminIDs: adminIDs,

		DefaultSubPrice:        os.Getenv("DEFAULT_SUB_PRICE"),
		DefaultSubDurationDays: envInt("DEFAULT_SUB_DURATION_DAYS", 30),
		DefaultSubTrafficGB:    int64(envInt("DEFAULT_SUB_TRAFFIC_GB", 0)),
		WebhookAddr:            envOrDefault("WEBHOOK_ADDR", ":8080"),
		PanelSubPort:           envInt("PANEL_SUB_PORT", 2096),

		DBHost:     os.Getenv("DB_HOST"),
		DBPort:     os.Getenv("DB_PORT"),
		DBUser:     os.Getenv("DB_USER"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBName:     os.Getenv("DB_NAME"),
		DBSSLMode:  envFirst("DB_SSL_MODE", "DB_SSLMODE"),

		PanelURL:       os.Getenv("PANEL_URL"),
		PanelUsername:  os.Getenv("PANEL_USERNAME"),
		PanelPassword:  os.Getenv("PANEL_PASSWORD"),
		CryptoAPIURL:   envFirst("CRYPTO_API_URL", "CRYPTOBOT_API_URL"),
		CryptoAPIToken: envFirst("CRYPTO_API_TOKEN", "CRYPTOBOT_TOKEN"),
	}

	return &config, nil

}

func parseAdminIDs(value string) ([]int64, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse ADMIN_IDS: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

// DatabaseDSN returns a safely escaped PostgreSQL connection string.
func (c Config) DatabaseDSN() string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.DBUser, c.DBPassword),
		Host:   net.JoinHostPort(c.DBHost, c.DBPort),
		Path:   c.DBName,
	}
	query := u.Query()
	query.Set("sslmode", c.DBSSLMode)
	u.RawQuery = query.Encode()
	return u.String()
}
