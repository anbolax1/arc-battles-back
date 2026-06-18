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
	"github.com/battle-for-respect/backend/internal/auth"
	"github.com/battle-for-respect/backend/internal/config"
	"github.com/battle-for-respect/backend/internal/db"
	"github.com/battle-for-respect/backend/internal/store"
	"github.com/battle-for-respect/backend/internal/ws"
)

func main() {
	cfg := config.Load()
	cfg.Validate()

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

	st := store.New(pool)

	// Бутстрап организатора: гарантируем аккаунт с ролью superadmin ДО приёма запросов,
	// чтобы организаторский логин нельзя было перехватить через открытую регистрацию.
	if cfg.SuperadminPassword != "" && cfg.SuperadminLogin != "" {
		hash, err := auth.HashPassword(cfg.SuperadminPassword)
		if err != nil {
			log.Fatalf("бутстрап организатора: хеш пароля: %v", err)
		}
		if err := st.EnsureSuperadmin(ctx, cfg.SuperadminLogin, hash); err != nil {
			log.Fatalf("бутстрап организатора: %v", err)
		}
		log.Printf("организатор обеспечен: %s (роль superadmin)", cfg.SuperadminLogin)
	} else {
		log.Println("ВНИМАНИЕ: SUPERADMIN_PASSWORD не задан — аккаунт организатора при старте не создаётся")
	}

	srv := api.New(cfg, st, ws.NewHub())

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr(),
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
