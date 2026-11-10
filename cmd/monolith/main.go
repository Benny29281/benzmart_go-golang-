package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"benzmart/internal/core/auth"
	"benzmart/internal/platform/config"
	"benzmart/internal/platform/health"
	"benzmart/migrations"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("aplikasi berhenti", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	runMigrations := flag.Bool("migrate", false, "terapkan migrasi database lalu keluar")
	createAdmin := flag.Bool("create-admin", false, "buat admin dari ADMIN_NAME, ADMIN_EMAIL, ADMIN_PASSWORD lalu keluar")
	flag.Parse()
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	db, err := sql.Open("mysql", cfg.MySQLDSN)
	if err != nil {
		return errors.New("konfigurasi MySQL tidak valid")
	}
	defer db.Close()
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	if *runMigrations {
		if err := migrations.Up(cfg.MySQLDSN); err != nil {
			return err
		}
		logger.Info("migrasi database selesai")
		return nil
	}
	service, err := auth.NewService(&auth.MySQLRepository{DB: db}, cfg.JWTSecret)
	if err != nil {
		return err
	}
	if *createAdmin {
		_, err := service.Register(context.Background(), os.Getenv("ADMIN_NAME"), os.Getenv("ADMIN_EMAIL"), os.Getenv("ADMIN_PASSWORD"), "admin")
		if err != nil {
			return err
		}
		logger.Info("akun admin dibuat")
		return nil
	}
	cache := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr})
	defer cache.Close()
	checks := map[string]health.Check{
		"mysql": db.PingContext,
		"redis": func(ctx context.Context) error { return cache.Ping(ctx).Err() },
		"kafka": func(ctx context.Context) error {
			var lastErr error
			for _, broker := range cfg.KafkaBrokers {
				conn, err := kafka.DialContext(ctx, "tcp", broker)
				if err != nil {
					lastErr = err
					continue
				}
				if deadline, ok := ctx.Deadline(); ok {
					_ = conn.SetDeadline(deadline)
				}
				_, err = conn.Brokers()
				_ = conn.Close()
				if err == nil {
					return nil
				}
				lastErr = err
			}
			return lastErr
		},
	}
	// Refuse to start the API before migrations have been applied.
	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()
	if _, err := db.ExecContext(startupCtx, "SELECT id FROM users LIMIT 0"); err != nil {
		return errors.New("database belum siap; jalankan perintah -migrate dan periksa koneksi MySQL")
	}
	mux := http.NewServeMux()
	mux.Handle("/", health.Handler(checks))
	authHandler := &auth.Handler{Service: service, Limiter: auth.RedisLimiter{Client: cache}, Logger: logger}
	mux.Handle("/api/", authHandler.Routes())
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           auth.Correlation(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	logger.Info("BenzMart berjalan", "address", cfg.HTTPAddr)
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
