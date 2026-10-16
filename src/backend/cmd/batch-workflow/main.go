package main

import (
	"log"
	"os"

	"workflow-api/internal/app"
	"workflow-api/internal/router"
)

func main() {
	if err := app.Run(router.BatchWorkflowService); err != nil {
		log.Printf("batch-workflow-svc failed: %v", err)
		os.Exit(1)
	}
}
