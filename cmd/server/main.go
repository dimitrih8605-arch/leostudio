package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hirotomasato/leostudio/internal/config"
	"github.com/hirotomasato/leostudio/internal/leonardo"
	"github.com/hirotomasato/leostudio/internal/server"
	"github.com/hirotomasato/leostudio/internal/service"
	"github.com/hirotomasato/leostudio/internal/store"
)

// tryCDPAutoCatch calls the import server's /auto-refresh endpoint which
// grabs fresh cookies from Chrome CDP. Best-effort: only works when both
// import_server.py and Chrome (with active Leonardo session) are running.
func tryCDPAutoCatch() {
	resp, err := http.Get("http://127.0.0.1:8001/auto-refresh")
	if err != nil {
		log.Printf("[auto-refresh] CDP catch skipped (import server offline): %v", err)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	log.Printf("[auto-refresh] CDP catch result (status=%d): %s", resp.StatusCode, string(body))
}

func main() {
	cfg := config.Load()

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer st.Close()

	if err := st.Bootstrap(cfg.ModelFile); err != nil {
		log.Fatalf("bootstrap store: %v", err)
	}

	client := leonardo.New()
	svc := service.NewLeonardoPool(st, client)

	// Start the queue manager so enqueued jobs actually process.
	qm := service.NewQueueManager(svc, st, 0)
	qm.Start()
	if err := qm.ResumeFromStore(); err != nil {
		log.Printf("[queue] resume failed: %v", err)
	}

	// Background token refresh: keeps JWTs alive via better-auth session
	// cookies. Runs every 45 min (JWT TTL ~1h). On failure, tries CDP
	// auto-catch from Chrome as a fallback.
	go func() {
		time.Sleep(10 * time.Minute) // initial delay for startup
		ticker := time.NewTicker(45 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			log.Printf("[auto-refresh] starting periodic cookie session refresh")
			res, err := svc.RefreshCookieSessions()
			if err != nil {
				log.Printf("[auto-refresh] refresh failed: %v", err)
				continue
			}
			log.Printf("[auto-refresh] checked=%d ok=%d", res.Checked, res.OK)
			if res.Checked > res.OK {
				go tryCDPAutoCatch()
			}
		}
	}()

	srv := server.New(cfg, st, svc, qm)
	addr := cfg.Addr()
	log.Printf("leostudio listening on %s", addr)
	if err := srv.Run(addr); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
