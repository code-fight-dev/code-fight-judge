package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"

	"code-fight-judge/internal/app"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	application, err := app.New()
	if err != nil {
		log.Fatalf("app init failed: %v", err)
	}

	if err := application.Run(ctx); err != nil {
		log.Fatalf("app run failed: %v", err)
	}
} 
