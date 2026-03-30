package main

import (
	"controlnode/broker"
	"controlnode/config"
	"controlnode/daqnode"
	"controlnode/health"
	"controlnode/webclient"
	"flag"
	"log"
	"strings"
)

func main() {
	configPath := flag.String("config", "../nodeConfigs_0.0.2.xml", "path to nodeConfigs XML file")
	flag.Parse()

	// ── Parse XML config ──────────────────────────────────────────────────
	cfg, err := config.Parse(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded: broadcast %d Hz, WS port %d",
		cfg.Network.BroadcastRateHz, cfg.Network.WebSocketPort)

	// ── Build refDes → DAQ node map for command routing ───────────────────
	refDesMap := config.BuildRefDesMap(cfg)

	// ── Collect restart command refDes values ─────────────────────────────
	var restartRefDes []string
	for _, cmd := range cfg.CtrNode.Health.Commands {
		if strings.EqualFold(strings.TrimSpace(cmd.Role), "cmd-bool") {
			// Convention: any CTR command with "restart" in refDes triggers exit.
			if strings.Contains(strings.ToLower(cmd.RefDes), "restart") {
				restartRefDes = append(restartRefDes, strings.TrimSpace(cmd.RefDes))
			}
		}
	}

	// ── Build web client config JSON (sent to browsers on connect) ────────
	wcConfigJSON, err := config.BuildWebClientConfigJSON(cfg)
	if err != nil {
		log.Fatalf("build web client config JSON: %v", err)
	}

	// ── Create broker ─────────────────────────────────────────────────────
	b := broker.New(refDesMap, restartRefDes)
	go b.Run(cfg.Network.BroadcastRateHz)

	// ── Health publisher ──────────────────────────────────────────────────
	sensorRefDes := buildHealthSensorMap(cfg)
	if len(sensorRefDes) > 0 {
		hp := health.New(b, sensorRefDes)
		go hp.Run(cfg.Network.BroadcastRateHz)
	}

	// ── DAQ node clients (one goroutine per enabled DAQ node) ─────────────
	for i := range cfg.DaqNodes.Nodes {
		node := &cfg.DaqNodes.Nodes[i]
		if !isEnabled(node.Enabled) {
			log.Printf("daqnode %s: disabled, skipping", node.RefDes)
			continue
		}
		if node.WSPort == 0 {
			log.Printf("daqnode %s: no wsPort configured, skipping", node.RefDes)
			continue
		}
		nodeConfigJSON, err := config.BuildDaqNodeConfigJSON(cfg, node.RefDes, cfg.Network.BroadcastRateHz)
		if err != nil {
			log.Fatalf("build DAQ node config JSON for %s: %v", node.RefDes, err)
		}
		client := daqnode.New(node.RefDes, node.IP, node.WSPort, nodeConfigJSON, b)
		go client.Run()
		log.Printf("daqnode %s: client started → ws://%s:%d", node.RefDes, node.IP, node.WSPort)
	}

	// ── Web client WebSocket server (blocks forever) ──────────────────────
	srv := webclient.New(cfg.Network.WebSocketPort, wcConfigJSON, b)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("webclient server: %v", err)
	}
}

// buildHealthSensorMap maps the well-known metric keys to refDes values from
// the <ctrNode><health><sensors> XML section, by matching keywords in the refDes.
func buildHealthSensorMap(cfg *config.SystemConfig) map[string]string {
	m := make(map[string]string)
	for _, s := range cfg.CtrNode.Health.Sensors {
		rd := strings.TrimSpace(s.RefDes)
		lower := strings.ToLower(rd)
		switch {
		case strings.Contains(lower, "uptime"):
			m["uptime"] = rd
		case strings.Contains(lower, "looptime") || strings.Contains(lower, "loop-time") || strings.Contains(lower, "loop_time"):
			m["loopTime"] = rd
		case strings.Contains(lower, "daqconnected") || strings.Contains(lower, "daq-connected") || strings.Contains(lower, "daq_connected"):
			m["daqConnected"] = rd
		case strings.Contains(lower, "wcconnected") || strings.Contains(lower, "wc-connected") || strings.Contains(lower, "wc_connected"):
			m["wcConnected"] = rd
		}
	}
	return m
}

func isEnabled(s string) bool {
	return strings.EqualFold(strings.TrimSpace(s), "true")
}
