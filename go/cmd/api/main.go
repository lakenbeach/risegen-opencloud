package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/lakenbeach/risegen-opencloud/go/cmd/api/handlers"
)

var logger *slog.Logger

func init() {
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
}

func main() {
	logger.Info("Starting Go CDK HTTP server")

	// Registrar rotas usando handlers do pacote handlers
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy","service":"go-cdk-wrapper"}`))
	})

	// Rotas de storage
	http.HandleFunc("/upload", handlers.UploadHandler)
	http.HandleFunc("/download", handlers.DownloadHandler)

	// Rota de métricas
	http.HandleFunc("/metrics", handlers.MetricsHandler)

	port := ":8081"
	logger.Info("Server listening on port", "port", port)
	if err := http.ListenAndServe(port, nil); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
