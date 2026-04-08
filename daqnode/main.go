package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	configPath := flag.String("config", "daqnode.yaml", "path to daqnode.yaml")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	log.Printf("DAQ node starting: refDes=%s port=%d (simulation mode)", cfg.RefDes, cfg.ListenPort)

	driver := newSimDriver()
	srv := newServer(cfg.RefDes, cfg.ListenPort, driver)

	// Graceful shutdown on Ctrl+C / SIGTERM
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		log.Printf("shutting down...")
		driver.Stop()
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
