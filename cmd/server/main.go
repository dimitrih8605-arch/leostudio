package main

import (
	"log"
	"os"

	"github.com/hirotomasato/leostudio/internal/config"
	"github.com/hirotomasato/leostudio/internal/leonardo"
	"github.com/hirotomasato/leostudio/internal/server"
	"github.com/hirotomasato/leostudio/internal/service"
	"github.com/hirotomasato/leostudio/internal/store"
)

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

	srv := server.New(cfg, st, svc)
	addr := cfg.Addr()
	log.Printf("leostudio listening on %s", addr)
	if err := srv.Run(addr); err != nil {
		log.Printf("server stopped: %v", err)
		os.Exit(1)
	}
}
