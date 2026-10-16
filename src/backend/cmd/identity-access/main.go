package main

import (
	"log"
	"os"

	"workflow-api/internal/app"
	"workflow-api/internal/router"
)

func main() {
	if err := app.Run(router.IdentityAccessService); err != nil {
		log.Printf("identity-access-svc failed: %v", err)
		os.Exit(1)
	}
}
