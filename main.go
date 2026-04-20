package main

import (
	"bufio"
	"container/ring"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// Config Settings
const (
	DBName     = "state.db"
	ConfigFile = "current_target.json"
	Port       = ":8080"
)

// Config Struct
type TargetConfig struct {
	Address   string `json:"target_address"`
	PublicKey string `json:"target_public_key"`
	Start     string `json:"range_start"`
	End       string `json:"range_end"`
	Status    string `json:"status"`
}

// Global State
var (
	db           *sql.DB
	logBuffer    *ring.Ring = ring.New(300)
	logMutex     sync.Mutex
	gpuPower     string = "--W"
	currentSpeed float64
	cmdWorker    *exec.Cmd
	workerMutex  sync.Mutex
	restartChan  = make(chan bool, 1)
)

// -- Initialization ----------------------------------------------
func initDB() {
	var err error
	db, err = sql.Open("sqlite", DBName+"?_pragma=journal_mode(WAL)")
	if err != nil {
		panic(err)
	}

	query := `
	CREATE TABLE IF NOT EXISTS search_state (
		target_pubkey TEXT PRIMARY KEY,
		last_start_hex TEXT,
		keys_scanned BIGINT,
		last_updated REAL
	);
	CREATE TABLE IF NOT EXISTS hits (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		private_key TEXT UNIQUE,
		target_address TEXT,
		found_at REAL
	);
	CREATE TABLE IF NOT EXISTS agent_telemetry (
		agent_id TEXT PRIMARY KEY, status TEXT, speed REAL
	);
	CREATE TABLE IF NOT EXISTS distinguished_points (
		point_value TEXT PRIMARY KEY, kangaroo_type INTEGER, distance_value TEXT
	);
	`
	_, err = db.Exec(query)
	if err != nil {
		fmt.Println("[SYS] Error initializing DB:", err)
	}
}

// -- Helpers ---------------------------------------------------
func addLog(source, msg string) {
	logMutex.Lock()
	defer logMutex.Unlock()
	ts := time.Now().Format("15:04:05")
	formatted := fmt.Sprintf("[%s] [%s] %s", ts, source, msg)
	logBuffer.Value = formatted
	logBuffer = logBuffer.Next()
	fmt.Println(formatted)
}

func getLogs() []string {
	logMutex.Lock()
	defer logMutex.Unlock()
	logs := []string{}
	logBuffer.Do(func(p interface{}) {
		if p != nil {
			logs = append(logs, p.(string))
		}
	})
	return logs
}

func clearLogs() {
	logMutex.Lock()
	defer logMutex.Unlock()
	logBuffer = ring.New(300)
}

func loadConfig() (*TargetConfig, error) {
	data, err := os.ReadFile(ConfigFile)
	if err != nil {
		return nil, err
	}
	var c TargetConfig
	err = json.Unmarshal(data, &c)
	return &c, err
}

func saveConfig(c *TargetConfig) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ConfigFile, data, 0644)
}

// -- Background Monitors ---------------------------------------
func gpuPowerMonitor() {
	for {
		cmd := exec.Command("nvidia-smi", "--query-gpu=power.draw", "--format=csv,noheader,nounits")
		out, err := cmd.Output()
		if err == nil {
			gpuPower = strings.TrimSpace(string(out)) + "W"
		}
		time.Sleep(3 * time.Second)
	}
}

func brainOrchestrator() {
	for {
		cfg, err := loadConfig()
		if err == nil && len(cfg.PublicKey) > 2 {
			// Get x-coordinate (remove 02/03 prefix)
			targetX := cfg.PublicKey[2:]
			var distance string
			err := db.QueryRow("SELECT distance_value FROM distinguished_points WHERE point_value = ?", targetX).Scan(&distance)
			if err == nil {
				addLog("BRAIN", "Collision Found! Data: "+distance)
				f, _ := os.OpenFile("hit.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				f.WriteString("DP HIT: " + distance + "\n")
				f.Close()
			}
		}
		time.Sleep(10 * time.Second)
	}
}

// -- Worker Manager (Kangaroo) ---------------------------------
func killWorker() {
	workerMutex.Lock()
	defer workerMutex.Unlock()
	if cmdWorker != nil && cmdWorker.Process != nil {
		exec.Command("taskkill", "/F", "/IM", "kangaroo.exe", "/T").Run()
		cmdWorker = nil
	}
}

func startWorker() {
	killWorker()

	cfg, err := loadConfig()
	if err != nil || cfg.Status != "RUNNING" {
		addLog("SYS", "Engine idle. Waiting for deployment.")
		return
	}

	var scannedBefore uint64 = 0
	effectiveStart := cfg.Start

	// Look up search state for persistence
	row := db.QueryRow("SELECT last_start_hex, keys_scanned FROM search_state WHERE target_pubkey = ?", cfg.PublicKey)
	err = row.Scan(&effectiveStart, &scannedBefore)
	if err == nil {
		addLog("ENG", "Resuming from local checkpoint: "+effectiveStart)
	} else {
		addLog("ENG", "New search initialized. Start range: "+cfg.Start)
	}

	cmd := exec.Command("./kangaroo.exe", effectiveStart, cfg.End, cfg.PublicKey)
	
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = cmd.Stdout

	workerMutex.Lock()
	cmdWorker = cmd
	workerMutex.Unlock()

	err = cmd.Start()
	if err != nil {
		addLog("SYS", "Failed to start kangaroo.exe: "+err.Error())
		return
	}

	addLog("SYS", "Worker bound to GPU.")

	scanner := bufio.NewScanner(stdout)
	lastSpdUpdate := time.Now()
	var lastCount uint64 = 0
	lastDBFlush := time.Now()

	// Using big.Int for hex arithmetic (saving state)
	origStartInt := new(big.Int)
	origStartInt.SetString(cfg.Start, 16)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "SPD:") {
			parts := strings.Split(line, ":")
			if len(parts) >= 2 {
				totalSessionSteps, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 16, 64)
				
				now := time.Now()
				elapsed := now.Sub(lastSpdUpdate).Seconds()

				if elapsed >= 1 {
					speed := float64(totalSessionSteps-lastCount) / elapsed
					currentSpeed = speed

					db.Exec("INSERT OR REPLACE INTO agent_telemetry (agent_id, status, speed) VALUES (?, ?, ?)", "GPU_WORKER", "Escaneando", speed)

					// Flush DB every 10 seconds
					if now.Sub(lastDBFlush).Seconds() > 10 {
						currentTotalScanned := scannedBefore + totalSessionSteps
						
						currentStepsInt := new(big.Int).SetUint64(currentTotalScanned)
						currentPosInt := new(big.Int).Add(origStartInt, currentStepsInt)
						currentPosHex := fmt.Sprintf("%x", currentPosInt)

						db.Exec(`INSERT OR REPLACE INTO search_state (target_pubkey, last_start_hex, keys_scanned, last_updated) VALUES (?, ?, ?, ?)`, 
							cfg.PublicKey, currentPosHex, currentTotalScanned, float64(now.Unix()))
						
						lastDBFlush = now
					}
					lastSpdUpdate = now
					lastCount = totalSessionSteps
				}
			}
		} else if strings.Contains(line, "HIT:") || strings.Contains(line, "FOUND") {
			priv := ""
			if strings.HasPrefix(line, "HIT:") {
				priv = strings.TrimSpace(strings.Split(line, ":")[1])
			} else {
				// Parse [SKW HIT] *** PRIVATE KEY FOUND *** -> hex | Address
				parts := strings.Split(line, "->")
				if len(parts) == 2 {
					priv = strings.TrimSpace(strings.Split(parts[1], "|")[0])
				}
			}
			
			if priv != "" {
				addLog("HIT", "PRIVATE KEY FOUND: "+priv)
				db.Exec("INSERT OR IGNORE INTO hits (private_key, target_address, found_at) VALUES (?, ?, ?)", priv, cfg.Address, float64(time.Now().Unix()))
				
				// Halt engine
				cfg.Status = "STOPPED"
				saveConfig(cfg)
				go killWorker()
			}
		} else {
			addLog("ENG", line)
		}
	}

	cmd.Wait()
	addLog("ENG", "Process terminated.")
	currentSpeed = 0
}

func workerSupervisor() {
	for {
		startWorker()
		<-restartChan // Wait for a signal to restart
	}
}

// -- HTTP Server -----------------------------------------------
func serveDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "dashboard.html")
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	var body TargetConfig
	err := json.NewDecoder(r.Body).Decode(&body)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	body.Status = "RUNNING"
	saveConfig(&body)
	
	clearLogs()
	addLog("SYS", "Novo alvo implantado: "+body.Address)
	
	select { case restartChan <- true: default: }
	w.WriteHeader(200)
}

func handleTerminate(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfig()
	if err == nil {
		cfg.Status = "STOPPED"
		saveConfig(cfg)
	}
	
	killWorker()
	addLog("SYS", "Worker parado pelo usuário.")
	w.WriteHeader(200)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
	stats := map[string]interface{}{
		"config":        map[string]string{},
		"speed":         currentSpeed,
		"power":         gpuPower,
		"logs":          getLogs(),
		"hits":          []map[string]interface{}{},
		"total_scanned": 0,
	}

	cfg, err := loadConfig()
	if err == nil {
		stats["config"] = cfg
		var keysScanned uint64
		err = db.QueryRow("SELECT keys_scanned FROM search_state WHERE target_pubkey = ?", cfg.PublicKey).Scan(&keysScanned)
		if err == nil {
			stats["total_scanned"] = keysScanned
		}
	}

	rows, err := db.Query("SELECT private_key, target_address, found_at FROM hits ORDER BY found_at DESC LIMIT 20")
	if err == nil {
		defer rows.Close()
		var hits []map[string]interface{}
		for rows.Next() {
			var pk, addr string
			var ts float64
			rows.Scan(&pk, &addr, &ts)
			hits = append(hits, map[string]interface{}{
				"private_key": pk,
				"address":     addr,
				"found_at":    ts,
			})
		}
		stats["hits"] = hits
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	fmt.Println("--- SECP256 KEY WORKER: DASHBOARD (GOLANG CORE) ---")
	
	initDB()
	defer db.Close()

	// Initial reset config
	if _, err := os.Stat(ConfigFile); os.IsNotExist(err) {
		saveConfig(&TargetConfig{Status: "STOPPED"})
	}

	// Start internal routines
	go gpuPowerMonitor()
	go brainOrchestrator()
	go workerSupervisor()

	// API Routes
	http.HandleFunc("/", serveDashboard)
	http.HandleFunc("/api/stats", handleStats)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/terminate", handleTerminate)
	http.HandleFunc("/api/clear_logs", func(w http.ResponseWriter, r *http.Request) {
		clearLogs()
		w.WriteHeader(200)
	})

	fmt.Println("[SYS] Dashboard Backend Running on http://localhost" + Port)
	http.ListenAndServe(Port, nil)
}
