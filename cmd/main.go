package main

import (
	"fmt"
	"log"

	"vpn-bot/internal/config"

)

func main() {
	config, err := config.Load()
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	fmt.Println(config)
}
