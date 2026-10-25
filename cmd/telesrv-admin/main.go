package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/config"
	"telesrv/internal/domain"
	"telesrv/internal/hoststats"
)

const (
	defaultAdminAPIAddr   = "127.0.0.1:2599"
	hostStatsPollInterval = 5 * time.Second
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pool.Close()

	hs := hoststats.NewPoller(cfg.DiskStatsPath)
	go hs.Run(ctx, hostStatsPollInterval)

	srv, err := newServer(cfg, newReadStore(pool), hs)
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("telesrv-admin listening on %s", cfg.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

type uiConfig struct {
	Addr          string
	PostgresDSN   string
	AdminAPIURL   string
	AdminAPIToken string
	Password      string
	Token         string
	SessionKey    []byte
	// DiskStatsPath points the dashboard host-disk sampler at the local path
	// that matters for the selected blob backend: permanent localfs storage or
	// the S3 upload spool.
	DiskStatsPath string
	// Permissions is the right set a panel session is issued with.
	Permissions []string
	// IdentityDir must point at the same directory every Core instance reads
	// (identity.dir in core.yaml and admin.yaml), so an identity edit here is
	// picked up by the Core identity watcher without a restart.
	IdentityDir string
	// WelcomeMessagePhoneDefault/WelcomeMessageEmailDefault/LoginCodeMessageDefault
	// are the compiled-in templates Core falls back to whenever the identity
	// panel override is unset; v2 role YAML exposes no override for them.
	WelcomeMessagePhoneDefault string
	WelcomeMessageEmailDefault string
	LoginCodeMessageDefault    string
}

// loadConfig reads the standalone admin YAML and converts it to uiConfig.
func loadConfig() (uiConfig, error) {
	appCfg, err := config.LoadAdmin()
	if err != nil {
		return uiConfig{}, fmt.Errorf("load config: %w", err)
	}

	sum := sha256.Sum256([]byte(appCfg.SessionKey))

	return uiConfig{
		Addr:          appCfg.Addr,
		PostgresDSN:   appCfg.PostgresDSN,
		AdminAPIURL:   adminAPIURL(appCfg.AdminAPIAddr),
		AdminAPIToken: appCfg.AdminAPIToken,
		Password:      appCfg.Password,
		Token:         appCfg.Token,
		SessionKey:    sum[:],
		DiskStatsPath: appCfg.DiskStatsPath,
		Permissions:   appCfg.Permissions,
		IdentityDir:   appCfg.IdentityDir,

		WelcomeMessagePhoneDefault: domain.DefaultWelcomeMessagePhoneTemplate,
		WelcomeMessageEmailDefault: domain.DefaultWelcomeMessageEmailTemplate,
		LoginCodeMessageDefault:    domain.DefaultLoginCodeMessageTemplate,
	}, nil
}

func adminAPIURL(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		addr = defaultAdminAPIAddr
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	return "http://" + addr
}

func newCommandID(prefix string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return prefix + "-" + time.Now().UTC().Format("20060102T150405.000000000") + "-" + hex.EncodeToString(b[:])
}
