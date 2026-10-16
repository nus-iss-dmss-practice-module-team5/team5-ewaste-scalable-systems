package main

import (
	"log"
	"os"

	"workflow-api/internal/app"
	"workflow-api/internal/router"
)

func main() {
	if err := app.Run(router.ClaimLogisticsService); err != nil {
		log.Printf("claim-logistics-svc failed: %v", err)
		os.Exit(1)
	}
}
