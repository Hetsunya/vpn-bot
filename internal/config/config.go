package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	BOTtoken string
	BOTName  string

	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string
}

func Load() (*Config, error) {

	err := godotenv.Load()
	if err != nil {
		log.Fatalf("Error loading .env file: %v", err)
	}

	config := Config{
		BOTtoken: os.Getenv("BOT_TOKEN"),
		BOTName:  os.Getenv("BOT_NAME"),

		DBHost:     os.Getenv("DB_HOST"),
		DBPort:     os.Getenv("DB_PORT"),
		DBUser:     os.Getenv("DB_USER"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBName:     os.Getenv("DB_NAME"),
		DBSSLMode:  os.Getenv("DB_SSL_MODE"),
	}

	return &config, nil

}
