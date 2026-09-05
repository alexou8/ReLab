package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexou8/relab/internal/api"
	"github.com/alexou8/relab/internal/config"
	"github.com/alexou8/relab/internal/engine"
)

// serveAPI runs the HTTP API until ctx ends, then drains in-flight requests.
//
// The bind address is validated against the API configuration before anything
// listens. An unauthenticated ReLab reachable from a network is the failure
// this project would most regret shipping, so it is refused at startup — with
// an error naming both ways out — rather than left to whoever reads SECURITY.md.
func serveAPI(ctx context.Context, eng *engine.Engine, addr string, log *slog.Logger, version string) error {
	apiConfig, err := config.APIConfigFromEnv()
	if err != nil {
		return err
	}
	if err := apiConfig.ValidateBind(addr); err != nil {
		return err
	}
	if len(apiConfig.Tokens) == 0 {
		log.WarnContext(ctx, "api is unauthenticated",
			"addr", addr,
			"set", config.EnvAPITokens,
			"detail", "anyone who can reach this address can read every run")
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: api.NewServer(eng, log, version, apiConfig).Routes(),
		// Without these a single stalled client holds a connection and a
		// goroutine indefinitely. ReadHeaderTimeout in particular is what
		// closes the slowloris hole.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.InfoContext(ctx, "api listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("api: listen on %s: %w", addr, err)
			return
		}
		errs <- nil
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		// The shutdown context is detached from the one that just ended, or
		// Shutdown would return immediately and drop in-flight requests.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("api: shutdown: %w", err)
		}
		return nil
	}
}
