package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/battle-for-respect/backend/internal/api"
	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/battle-for-respect/backend/internal/ws"
)

func main() {
	cfg := config.Load()

	log.Println("прогон миграций…")
	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("миграции: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("подключение к БД: %v", err)
	}
	defer pool.Close()

	srv := api.New(cfg, store.New(pool), ws.NewHub())

	httpServer := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("API слушает на :%s", cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("останавливаюсь…")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutCtx)
}
