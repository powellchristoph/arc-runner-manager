package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"path/filepath"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/powellchristoph/arc-runner-managerinternal/api"
	helmclient "github.com/powellchristoph/arc-runner-managerinternal/helm"
	"github.com/powellchristoph/arc-runner-managerinternal/k8s"
	authmiddleware "github.com/powellchristoph/arc-runner-managerinternal/middleware"
	"github.com/powellchristoph/arc-runner-managerpkg/config"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "err", err)
		os.Exit(1)
	}

	// Auth: prefer in-cluster, fall back to kubeconfig for local dev.
	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		k8sCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Error("failed to build kubeconfig", "path", kubeconfig, "err", err)
			os.Exit(1)
		}
		logger.Info("using kubeconfig", "path", kubeconfig)
	}

	k8sClient, err := k8s.NewClient(k8sCfg, logger)
	if err != nil {
		logger.Error("failed to create k8s client", "err", err)
		os.Exit(1)
	}

	helmClient := helmclient.NewClient(cfg, k8sCfg, logger)

	handler := api.NewHandler(cfg, helmClient, k8sClient, logger)

	r := chi.NewRouter()
	r.Use(chimiddleware.RequestID)
	r.Use(chimiddleware.RealIP)
	r.Use(chimiddleware.Logger)
	r.Use(chimiddleware.Recoverer)
	r.Use(chimiddleware.Timeout(60 * time.Second))

	// /healthz is unauthenticated — register before auth middleware.
	r.Get("/healthz", handler.Healthz)

	// All /api/v1 routes require Bearer token auth.
	r.Group(func(r chi.Router) {
		tokenStore := authmiddleware.NewTokenStore(cfg.APITokens)
		r.Use(authmiddleware.BearerAuth(tokenStore))
		handler.RegisterRoutes(r)
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 90 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGTERM / SIGINT.
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("server starting", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	<-done
	logger.Info("shutting down server")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("server shutdown error", "err", err)
	}

}
