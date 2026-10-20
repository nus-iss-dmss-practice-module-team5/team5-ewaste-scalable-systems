package main

import (
	"log"
	"os"

	"workflow-api/internal/app"
	"workflow-api/internal/router"
)

func main() {
	if err := app.Run(router.CustodyVerificationService); err != nil {
		log.Printf("custody-verification-svc failed: %v", err)
		os.Exit(1)
	}
}
