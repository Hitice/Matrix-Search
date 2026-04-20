package main

import (
	"bufio"
	"flag"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

func main() {
	flag.Parse()
	fmt.Printf("--- SECP256 KEY WORKER: POOL MINER ---\n")
	fmt.Printf("[SYS] Connecting to Pool: %s\n", *poolAddr)
	fmt.Printf("[SYS] Wallet/ID: %s\n", *wallet)

	for {
		url := fmt.Sprintf("%s?wallet=%s", *poolAddr, *wallet)
		fmt.Printf("[SYS] Connecting to Pool: %s\n", url)
		
		conn, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			fmt.Printf("[Error] Cannot connect to pool (%s). Retrying in 5s...\n", err.Error())
			time.Sleep(5 * time.Second)
			continue
		}
		
		fmt.Println("[SYS] Connected to Pool successfully. Waiting for jobs...")
		mineLoop(conn)
		
		fmt.Println("[SYS] Connection lost. Reconnecting in 5s...")
		time.Sleep(5 * time.Second)
	}
}

func mineLoop(conn *websocket.Conn) {
	defer conn.Close()

	for {
		var job WSMessage
		err := conn.ReadJSON(&job)
		if err != nil {
			fmt.Println("[SYS] Disconnected from pool.")
			return
		}

		if job.Cmd == "IDLE" {
			time.Sleep(5 * time.Second)
			continue
		}

		if job.Cmd == "WORK" {
			fmt.Printf("[MINER] Received Job: %s -> %s\n", job.Start[:8]+"...", job.End[:8]+"...")
			
			cmd := exec.Command("./kangaroo.exe", job.Start, job.End, job.PubKey)
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

						if elapsed >= 2 { // Report progress every 2s
							speed := float64(totalSteps-lastCount) / elapsed
							conn.WriteJSON(map[string]interface{}{
								"event": "PROGRESS",
								"speed": speed,
							})
							fmt.Printf("[MINER] Speed: %.2f MKeys/s\n", speed)
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
						fmt.Println("\n[!!!] PRIVATE KEY FOUND! SUBMITTING TO POOL...")
						fmt.Println(priv)
						conn.WriteJSON(map[string]interface{}{
							"event": "HIT",
							"priv":  priv,
						})
						exec.Command("taskkill", "/F", "/IM", "kangaroo.exe", "/T").Run()
						break
					}
				}
			}
			cmd.Wait()
			fmt.Println("[MINER] Job finished. Requesting new chunk...")
			
			// Tell the server we are ready for more (the server blocks until we reply)
			conn.WriteJSON(map[string]interface{}{"event": "PROGRESS", "speed": 0.0})
		}
	}
}
