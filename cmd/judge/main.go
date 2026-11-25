package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"code-fight-judge/internal/app"
	"code-fight-judge/internal/autoscale"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "autoscale" {
		if err := autoscale.Run(ctx, os.Args[2:]); err != nil {
			log.Fatalf("autoscaler run failed: %v", err)
		}
		return
	}

	application, err := app.New()
	if err != nil {
		log.Fatalf("app init failed: %v", err)
	}

	if err := application.Run(ctx); err != nil {
		log.Fatalf("app run failed: %v", err)
	}
}
