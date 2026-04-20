package main

import (
	"bufio"
	"flag"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	poolAddr = flag.String("pool", "ws://localhost:8080/mine", "Pool WebSocket Address")
	wallet   = flag.String("wallet", "AnonMiner", "Your payout wallet / machine ID")
)

type WSMessage struct {
	Cmd    string `json:"cmd"`
	Start  string `json:"start"`
	End    string `json:"end"`
	PubKey string `json:"pubkey"`
	Msg    string `json:"msg"`
}

func countGPUs() int {
	cmd := exec.Command("nvidia-smi", "-L")
	output, err := cmd.Output()
	if err != nil {
		fmt.Println("[SYS] No NVIDIA GPU detected or nvidia-smi not in PATH. Defaulting to 1.")
		return 1
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	count := 0
	for _, line := range lines {
		if strings.Contains(line, "GPU") {
			count++
		}
	}
	if count == 0 { return 1 }
	return count
}

func main() {
	flag.Parse()
	fmt.Printf("--- SECP256 KEY WORKER: POOL MINER (MULTI-GPU) ---\n")
	
	gpuCount := countGPUs()
	fmt.Printf("[SYS] Detected %d GPU(s). Starting workers...\n", gpuCount)

	var wg sync.WaitGroup
	for i := 0; i < gpuCount; i++ {
		wg.Add(1)
		workerName := fmt.Sprintf("%s_GPU%d", *wallet, i)
		deviceID := strconv.Itoa(i)
		go func(id string, name string) {
			defer wg.Done()
			runManager(id, name)
		}(deviceID, workerName)
	}
	wg.Wait()
}

func runManager(deviceID string, workerName string) {
	for {
		url := fmt.Sprintf("%s?wallet=%s", *poolAddr, workerName)
		fmt.Printf("[%s] Connecting to Pool...\n", workerName)
		
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			fmt.Printf("[%s] Connection error: %s. Retrying in 5s...\n", workerName, err.Error())
			time.Sleep(5 * time.Second)
			continue
		}
		
		fmt.Printf("[%s] Connected. Waiting for jobs...\n", workerName)
		mineLoop(conn, deviceID, workerName)
		
		fmt.Printf("[%s] Connection lost. Reconnecting in 5s...\n", workerName)
		time.Sleep(5 * time.Second)
	}
}

func mineLoop(conn *websocket.Conn, deviceID string, workerName string) {
	defer conn.Close()

	for {
		var job WSMessage
		err := conn.ReadJSON(&job)
		if err != nil {
			return
		}

		if job.Cmd == "IDLE" {
			time.Sleep(5 * time.Second)
			continue
		}

		if job.Cmd == "WORK" {
			fmt.Printf("[%s] Job Received: %s -> %s\n", workerName, job.Start[:8], job.End[:8])
			
			// Add deviceID as 4th argument
			cmd := exec.Command("./kangaroo.exe", job.Start, job.End, job.PubKey, deviceID)
			stdout, _ := cmd.StdoutPipe()
			cmd.Stderr = cmd.Stdout
			cmd.Start()

			scanner := bufio.NewScanner(stdout)
			lastSpdUpdate := time.Now()
			var lastCount uint64 = 0

			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" { continue }

				if strings.HasPrefix(line, "SPD:") {
					parts := strings.Split(line, ":")
					if len(parts) >= 2 {
						totalSteps, _ := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
						now := time.Now()
						elapsed := now.Sub(lastSpdUpdate).Seconds()

						if elapsed >= 2 {
							speed := float64(totalSteps-lastCount) / elapsed
							conn.WriteJSON(map[string]interface{}{
								"event": "PROGRESS",
								"speed": speed,
							})
							// Optional: fmt.Printf("[%s] Speed: %.2f MKeys/s\n", workerName, speed/1e6)
							lastSpdUpdate = now
							lastCount = totalSteps
						}
					}
				} else if strings.Contains(line, "HIT:") || strings.Contains(line, "FOUND") {
					priv := ""
					if strings.HasPrefix(line, "HIT:") {
						priv = strings.TrimSpace(strings.Split(line, ":")[1])
					} else {
						parts := strings.Split(line, "->")
						if len(parts) == 2 {
							priv = strings.TrimSpace(strings.Split(parts[1], "|")[0])
						}
					}
					
					if priv != "" {
						fmt.Printf("\n[%s] !!! PRIVATE KEY FOUND !!! -> %s\n", workerName, priv)
						conn.WriteJSON(map[string]interface{}{
							"event": "HIT",
							"priv":  priv,
						})
						// Stop kangaroo for THIS worker
						cmd.Process.Kill()
						break
					}
				}
			}
			cmd.Wait()
			conn.WriteJSON(map[string]interface{}{"event": "PROGRESS", "speed": 0.0})
		}
	}
}
