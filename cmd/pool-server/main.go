package main

import (
	"container/ring"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

const (
	DBName     = "state.db"
	ConfigFile = "current_target.json"
	Port       = ":8080"
	ChunkSize  = 10_000_000_000 // 10 Billion keys per chunk
)

type TargetConfig struct {
	Address   string `json:"target_address"`
	PublicKey string `json:"target_public_key"`
	Start     string `json:"range_start"`
	End       string `json:"range_end"`
	Status    string `json:"status"`
}

var (
	db          *sql.DB
	logBuffer   *ring.Ring = ring.New(300)
	logMutex    sync.Mutex
	activeMinerCount int
	minerMutex  sync.Mutex
	minerSpeeds map[string]float64 = make(map[string]float64)
	globalSpeed float64
	upgrader    = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	restartChan = make(chan bool, 1)
)

func initDB() {
	var err error
	db, err = sql.Open("sqlite", DBName+"?_pragma=journal_mode(WAL)")
	if err != nil {
		panic(err)
	}
	db.Exec(`
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
	`)
}

func addLog(source, msg string) {
	logMutex.Lock()
	defer logMutex.Unlock()
	ts := time.Now().Format("15:04:05")
	formatted := fmt.Sprintf("[%s] [%s] %s", ts, source, msg)

	if logBuffer.Value != nil && logBuffer.Value.(string) == formatted {
		return // anti-spam
	}

	logBuffer.Value = formatted
	logBuffer = logBuffer.Next()
	fmt.Println(formatted)
}

func getLogs() []string {
	logMutex.Lock()
	defer logMutex.Unlock()
	var logs []string
	logBuffer.Do(func(p interface{}) {
		if p != nil {
			logs = append(logs, p.(string))
		}
	})
	return logs
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

// ─── POOL CHUNK DISPATCHER ───
// Returns a new slice of work
func allocateChunk(cfg *TargetConfig) (string, string) {
	var lastAssigned string
	err := db.QueryRow("SELECT last_start_hex FROM search_state WHERE target_pubkey = ?", cfg.PublicKey).Scan(&lastAssigned)
	
	if err != nil {
		// New target
		lastAssigned = cfg.Start
	}

	startInt := new(big.Int)
	startInt.SetString(lastAssigned, 16)

	chunkInt := new(big.Int).SetUint64(ChunkSize)
	endChunkInt := new(big.Int).Add(startInt, chunkInt)

	startHex := fmt.Sprintf("%x", startInt)
	endHex := fmt.Sprintf("%x", endChunkInt)

	db.Exec(`INSERT OR REPLACE INTO search_state (target_pubkey, last_start_hex, keys_scanned, last_updated) VALUES (?, ?, ?, ?)`,
		cfg.PublicKey, endHex, 0, float64(time.Now().Unix())) // keys_scanned globally is handled in another routine if wanted

	return startHex, endHex
}

// ─── WEBSOCKET HANDLER ───
func wsMinerHub(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("[SYS] WS Upgrade failed:", err)
		return
	}
	defer conn.Close()

	minerID := r.URL.Query().Get("wallet")
	if minerID == "" {
		minerID = "AnonMiner"
	}

	minerMutex.Lock()
	activeMinerCount++
	minerMutex.Unlock()
	addLog("POOL", "Miner Joined: "+minerID)

	defer func() {
		minerMutex.Lock()
		activeMinerCount--
		delete(minerSpeeds, minerID)
		minerMutex.Unlock()
		addLog("POOL", "Miner Disconnected: "+minerID)
	}()

	for {
		cfg, err := loadConfig()
		if err != nil || cfg.Status != "RUNNING" {
			conn.WriteJSON(map[string]interface{}{"cmd": "IDLE", "msg": "No active target"})
			time.Sleep(5 * time.Second)
			continue
		}

		start, end := allocateChunk(cfg)
		addLog("POOL", fmt.Sprintf("Assigned chunk to %s: %s -> %s", minerID, start[:8]+"...", end[:8]+"..."))

		err = conn.WriteJSON(map[string]interface{}{
			"cmd":    "WORK",
			"start":  start,
			"end":    end,
			"pubkey": cfg.PublicKey,
		})
		if err != nil {
			break
		}

		// Wait for result
		var result map[string]interface{}
		err = conn.ReadJSON(&result)
		if err != nil {
			break
		}

		if evt, ok := result["event"].(string); ok {
			if evt == "HIT" {
				priv := result["priv"].(string)
				addLog("HIT", "PRIVATE KEY FOUND BY "+minerID+": "+priv)
				db.Exec("INSERT OR IGNORE INTO hits (private_key, target_address, found_at) VALUES (?, ?, ?)", priv, cfg.Address, float64(time.Now().Unix()))
				cfg.Status = "STOPPED"
				saveConfig(cfg)
				
				f, _ := os.OpenFile("hit.txt", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
				f.WriteString("HIT BY " + minerID + " : " + priv + "\n")
				f.Close()
			} else if evt == "PROGRESS" {
				spd := result["speed"].(float64)
				minerMutex.Lock()
				minerSpeeds[minerID] = spd
				
				// Re-calculate global speed as sum
				total := 0.0
				for _, s := range minerSpeeds {
					total += s
				}
				globalSpeed = total
				minerMutex.Unlock()
			}
		}
	}
}

// ─── API HANDLERS ───
func handleStats(w http.ResponseWriter, r *http.Request) {
	stats := map[string]interface{}{
		"config":        map[string]string{},
		"speed":         globalSpeed,
		"power":         fmt.Sprintf("%d Workers", activeMinerCount), // Hijack power display for worker count
		"logs":          getLogs(),
		"hits":          []map[string]interface{}{},
		"total_scanned": 0,
	}

	cfg, _ := loadConfig()
	stats["config"] = cfg

	rows, err := db.Query("SELECT private_key, target_address, found_at FROM hits ORDER BY found_at DESC LIMIT 20")
	if err == nil {
		defer rows.Close()
		var hits []map[string]interface{}
		for rows.Next() {
			var pk, addr string
			var ts float64
			rows.Scan(&pk, &addr, &ts)
			hits = append(hits, map[string]interface{}{"private_key": pk, "address": addr, "found_at": ts})
		}
		stats["hits"] = hits
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	var body TargetConfig
	json.NewDecoder(r.Body).Decode(&body)
	body.Status = "RUNNING"
	saveConfig(&body)
	logBuffer = ring.New(300)
	addLog("SYS", "New target deployed (Pool broadcasting...): "+body.Address)
	w.WriteHeader(200)
}

func handleTerminate(w http.ResponseWriter, r *http.Request) {
	cfg, err := loadConfig()
	if err == nil {
		cfg.Status = "STOPPED"
		saveConfig(cfg)
	}
	addLog("SYS", "Broadcast stop signal to all miners.")
	w.WriteHeader(200)
}

func serveDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "dashboard.html")
}

func main() {
	fmt.Println("--- SECP256 KEY WORKER: POOL MASTER ---")
	initDB()
	defer db.Close()
	if _, err := os.Stat(ConfigFile); os.IsNotExist(err) {
		saveConfig(&TargetConfig{Status: "STOPPED"})
	}

	http.HandleFunc("/", serveDashboard)
	http.HandleFunc("/mine", wsMinerHub) // Miner endpoint
	http.HandleFunc("/api/stats", handleStats)
	http.HandleFunc("/api/config", handleConfig)
	http.HandleFunc("/api/terminate", handleTerminate)

	fmt.Println("[SYS] Pool Master running on http://localhost" + Port)
	fmt.Println("[SYS] WebSocket endpoint waiting for miners at ws://localhost" + Port + "/mine")
	http.ListenAndServe(Port, nil)
}
