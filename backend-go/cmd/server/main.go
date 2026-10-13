package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/api"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/login"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/notify"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/ondemand"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/push"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "notify-test":
			return runNotifyTest()
		default:
			return fmt.Errorf("unknown command %q (the only command is notify-test; run with no arguments to start the server)", os.Args[1])
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		return err
	}

	if len(cfg.Tokens) == 0 {
		log.Warn("API_TOKENS is empty: ingest endpoints will reject every request")
	}
	apiServer := api.NewServer(store.New(pool), api.NewAuthenticator(cfg.Tokens), cfg.Timezone, log)
	apiServer.SetIngestRate(cfg.IngestPerMinute)

	ocfg, err := ondemand.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid on-demand test settings: %w", err)
	}
	manager := ondemand.NewManager(ocfg, store.New(pool), nil, log)
	apiServer.SetOnDemand(manager, ocfg.TrustProxy)
	manager.Start(ctx)
	go manager.Run(ctx)
	if ok, why := ocfg.OoklaStatus(nil); ok {
		log.Info("on-demand tests ready", "ookla", "on", "device", ocfg.DeviceName)
	} else {
		log.Info("on-demand tests: Ookla is off", "why", why, "ndt7", "on (runs in the visitor's browser)")
	}

	lcfg, loginOn, err := login.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid login settings: %w", err)
	}
	if loginOn {
		sessions, err := login.New(ctx, lcfg, store.New(pool))
		if err != nil {
			return err
		}
		apiServer.SetLogin(sessions)
		log.Info("dashboard login on", "session_days", int(lcfg.Lifetime.Hours()/24))
	} else {
		log.Info("dashboard login off: anyone who can reach the dashboard can open it (set DASHBOARD_PASSWORD to turn the login on)")
	}

	ncfg, err := notify.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid ntfy settings: %w", err)
	}
	// Browser and installed-app notifications (Web Push) ride on the same alerts as ntfy.
	var pushSvc *push.Service
	pcfg, pushOn, err := push.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid push settings: %w", err)
	}
	if pushOn {
		if pushSvc, err = push.New(ctx, pcfg, store.New(pool), log); err != nil {
			return err
		}
		apiServer.SetPush(pushSvc)
		log.Info("browser notifications (web push) on")
	} else {
		log.Info("browser notifications (web push) off (PUSH_ENABLED=no)")
	}
	if ncfg.Enabled {
		var sender notify.Sender = notify.NewNtfy(ncfg)
		if pushSvc != nil {
			sender = notify.Fanout(sender, pushSvc)
		}
		monitor := notify.NewMonitor(ncfg, store.New(pool), sender, log)
		if err := monitor.Prepare(ctx); err != nil {
			return err
		}
		apiServer.SetNotifications(monitor)
		go monitor.Run(ctx)
		log.Info("notifications on", "ntfy", ncfg.Host(), "check_every", ncfg.CheckEvery.String())
	} else {
		log.Info("alerts are off (set NTFY_TOPIC to turn them on); browser notifications can only send tests")
	}

	if cfg.StaticDir != "" {
		if _, err := os.Stat(cfg.StaticDir); err != nil {
			return err
		}
		apiServer.ServeStatic(os.DirFS(cfg.StaticDir))
		log.Info("serving frontend", "dir", cfg.StaticDir)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.ListenAddr, "timezone", cfg.Timezone)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// runNotifyTest sends one message with the configured ntfy settings, so a setup can be checked without waiting for a
// real problem: `server notify-test`, or `docker compose run backend notify-test`. It needs no database.
func runNotifyTest() error {
	cfg, err := notify.FromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("invalid ntfy settings: %w", err)
	}
	if !cfg.Enabled {
		return errors.New("NTFY_TOPIC is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := notify.SendTest(ctx, notify.NewNtfy(cfg)); err != nil {
		return fmt.Errorf("the test notification could not be delivered: %w", err)
	}
	fmt.Printf("Sent a test notification through %s. Check your phone.\n", cfg.Host())
	return nil
}
