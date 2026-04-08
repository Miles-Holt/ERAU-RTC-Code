package main

import (
	"controlnode/broker"
	"controlnode/config"
	"controlnode/daqnode"
	"controlnode/health"
	"controlnode/softchan"
	"controlnode/webclient"
	"encoding/json"
	"flag"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	configDir := flag.String("config-dir", "../config", "path to config directory")
	webRoot := flag.String("webroot", "", "directory to serve as web client UI (empty = use embedded)")
	flag.Parse()

	// Strip the "static/" prefix from the embedded FS so index.html is at the root.
	embeddedSub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("embedded FS sub: %v", err)
	}

	// ── Parse YAML config ─────────────────────────────────────────────────
	cfg, err := config.ParseDir(*configDir)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("config loaded: broadcast %d Hz, WS port %d",
		cfg.Network.BroadcastRateHz, cfg.Network.WebSocketPort)

	// ── Build refDes → DAQ node map for command routing ───────────────────
	refDesMap := config.BuildRefDesMap(cfg)

	// ── Collect restart command refDes values ─────────────────────────────
	var restartRefDes []string
	var allCtrCmdRefDes []string
	for _, cmd := range cfg.CtrNode.Health.Commands {
		rd := strings.TrimSpace(cmd.RefDes)
		if strings.EqualFold(strings.TrimSpace(cmd.Role), "cmd-bool") {
			allCtrCmdRefDes = append(allCtrCmdRefDes, rd)
			// Convention: any CTR command with "restart" in refDes triggers exit.
			if strings.Contains(strings.ToLower(rd), "restart") {
				restartRefDes = append(restartRefDes, rd)
			}
		}
	}

	// ── Build web client config JSON (sent to browsers on connect) ────────
	wcConfigJSON, err := config.BuildWebClientConfigJSON(cfg)
	if err != nil {
		log.Fatalf("build web client config JSON: %v", err)
	}

	// ── Software channels (load before broker so refDesMap is complete) ─────
	var softchanConfigJSON []byte
	sc, scErr := softchan.New(
		filepath.Join(*configDir, "softChannels.yaml"),
		filepath.Join(*configDir, "softChannelValues.yaml"),
	)
	if scErr != nil {
		log.Printf("softchan: failed to load, continuing without: %v", scErr)
	} else {
		for k, v := range sc.RefDesMap() {
			refDesMap[k] = v
		}
		softchanConfigJSON = sc.ConfigJSON()
	}

	// ── Build DAQ control state machine config (sent to browsers) ────────
	stateConfigJSON := config.BuildStateConfigJSON(cfg.DaqControls)
	if stateConfigJSON != nil {
		log.Printf("state_config: loaded %d DAQ control definition(s)", len(cfg.DaqControls))
	}

	// ── Create broker ─────────────────────────────────────────────────────
	b := broker.New(refDesMap, restartRefDes)
	go b.Run(cfg.Network.BroadcastRateHz)

	// ── Start software channel publisher/handler ───────────────────────────
	if sc != nil {
		go sc.Run(b, cfg.Network.BroadcastRateHz)
	}

	// ── Health publisher ──────────────────────────────────────────────────
	sensorRefDes := buildHealthSensorMap(cfg)
	if len(sensorRefDes) > 0 {
		hp := health.New(b, sensorRefDes, allCtrCmdRefDes)
		go hp.Run(cfg.Network.BroadcastRateHz)
	}

	// ── DAQ node clients (one goroutine per enabled DAQ node) ─────────────
	for i := range cfg.DaqNodes.Nodes {
		node := &cfg.DaqNodes.Nodes[i]
		if !node.Enabled {
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

	// ── Load front panel layout files ─────────────────────────────────────
	panelLayoutsPath := filepath.Join(*configDir, "panelLayouts.yaml")
	panelCfg, err := config.LoadPanelLayouts(panelLayoutsPath)
	if err != nil {
		log.Fatalf("panelLayouts.yaml: %v", err)
	}
	panelMessages := loadPanelMessages(panelCfg, *configDir)

	layoutPaths := make(map[string]string)
	for _, p := range panelCfg.Panels {
		if p.Enabled {
			layoutPaths[filepath.Base(p.File)] = filepath.Join(*configDir, p.File)
		}
	}

	// ── Load user auth config ─────────────────────────────────────────────
	authCfg, err := webclient.LoadUserAuth(filepath.Join(*configDir, "userAuth.yaml"))
	if err != nil {
		log.Printf("webclient: userAuth.yaml not loaded, auth disabled: %v", err)
		authCfg = nil
	}

	// ── Web client WebSocket server (blocks forever) ──────────────────────
	srv := webclient.New(cfg.Network.WebSocketPort, wcConfigJSON, softchanConfigJSON, stateConfigJSON, panelMessages, b, *webRoot, embeddedSub, authCfg, layoutPaths)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("webclient server: %v", err)
	}
}

// loadPanelMessages reads each enabled front-panel YAML from disk and builds
// the pid_layout JSON payloads that are sent to browsers on connect.
// File paths in panelLayouts.yaml are resolved relative to configDir.
func loadPanelMessages(cfg *config.PanelLayoutsConfig, configDir string) [][]byte {
	var msgs [][]byte
	for _, p := range cfg.Panels {
		if !p.Enabled {
			log.Printf("front panel %q: disabled, skipping", p.Name)
			continue
		}
		absPath := filepath.Join(configDir, p.File)
		content, err := os.ReadFile(absPath)
		if err != nil {
			log.Printf("front panel %q: read %s: %v", p.Name, absPath, err)
			continue
		}
		payload, err := json.Marshal(map[string]interface{}{
			"type":     "pid_layout",
			"name":     p.Name,
			"filename": filepath.Base(p.File),
			"content":  string(content),
		})
		if err != nil {
			log.Printf("front panel %q: marshal: %v", p.Name, err)
			continue
		}
		msgs = append(msgs, payload)
		log.Printf("front panel loaded: %q (%s)", p.Name, p.File)
	}
	return msgs
}

// buildHealthSensorMap maps well-known metric keys ("uptime", "loopTime",
// "daqConnected", "wcConnected") to their refDes values from controlNode.yaml
// by matching keywords in the refDes string.
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

