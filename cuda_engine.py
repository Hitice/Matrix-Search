import os
import sqlite3
import subprocess
import time
import json

# Configuration
DB_NAME = "state.db"
CONFIG_FILE = "current_target.json"

def load_config():
    if not os.path.exists(CONFIG_FILE):
        return None
    with open(CONFIG_FILE, 'r') as f:
        return json.load(f)

class SKW_Engine:
    def __init__(self):
        self.exe_path = "kangaroo.exe"
        self.last_spd_update = time.time()
        self.last_db_flush = time.time()
        self.last_count = 0
        
    def start_search(self):
        config = load_config()
        if not config or config.get('status') == 'STOPPED':
            print("[SKW Eng] Inativo: Aguardando implantação pelo Dashboard.")
            return

        pubkey      = config['target_public_key']
        orig_start  = config.get('range_start', '1')
        range_end   = config.get('range_end', 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff')
        
        # Connect to DB to check for resumed state
        conn = sqlite3.connect(DB_NAME, isolation_level=None)
        conn.execute('PRAGMA journal_mode=WAL')
        conn.execute('''
            CREATE TABLE IF NOT EXISTS search_state (
                target_pubkey TEXT PRIMARY KEY,
                last_start_hex TEXT,
                keys_scanned BIGINT,
                last_updated REAL
            )
        ''')
        
        # Load resume point
        cursor = conn.cursor()
        cursor.execute("SELECT last_start_hex, keys_scanned FROM search_state WHERE target_pubkey = ?", (pubkey,))
        row = cursor.fetchone()
        
        effective_start = orig_start
        cumulative_scanned_before = 0
        if row:
            effective_start = row[0]
            cumulative_scanned_before = row[1]
            print(f"[SKW Eng] Retomando de checkpoint anterior: {effective_start}")
        else:
            print(f"[SKW Eng] Nova busca inicializada: {orig_start}")

        print(f"[SKW Eng] Alvo: {pubkey}")
        print(f"[SKW Eng] Range Efetivo: {effective_start} -> {range_end}")
        
        proc = subprocess.Popen([self.exe_path, effective_start, range_end, pubkey], 
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True, bufsize=1)
        
        # Reset timers right at start of search to calibrate speed
        self.last_spd_update = time.time()
        self.last_count = 0
        self.last_db_flush = time.time()
        
        while True:
            line = proc.stdout.readline()
            if not line: break
            line = line.strip()
            if not line: continue
            
            try:
                if line.startswith("SPD:"):
                    parts = line.split(":")
                    if len(parts) >= 2:
                        total_steps_this_session = int(parts[1])
                        now = time.time()
                        elapsed = now - self.last_spd_update
                        
                        if elapsed >= 1:
                            speed = (total_steps_this_session - self.last_count) / elapsed
                            
                            # Update Telemetry (for Live dashboard)
                            conn.execute("INSERT OR REPLACE INTO agent_telemetry (agent_id, status, speed) VALUES (?, ?, ?)",
                                         ("GPU_WORKER", "Escaneando", speed))
                            
                            # Periodically save state to DB (every 10 seconds or every 10M keys)
                            if now - self.last_db_flush > 10:
                                current_scanned_total = cumulative_scanned_before + total_steps_this_session
                                
                                # Estimate current hex position
                                # (Original start + current total scanned)
                                try:
                                    current_pos_int = int(orig_start, 16) + current_scanned_total
                                    current_pos_hex = hex(current_pos_int)[2:]
                                    conn.execute("""
                                        INSERT OR REPLACE INTO search_state (target_pubkey, last_start_hex, keys_scanned, last_updated)
                                        VALUES (?, ?, ?, ?)
                                    """, (pubkey, current_pos_hex, current_scanned_total, now))
                                except: pass
                                
                                self.last_db_flush = now

                            self.last_spd_update = now
                            self.last_count = total_steps_this_session
                
                elif line.startswith("HIT:"):
                    parts = line.split(":")
                    if len(parts) >= 2:
                        priv = parts[1].strip()
                        target_addr = config.get('target_address', 'unknown')
                        conn.execute("INSERT OR IGNORE INTO hits (private_key, target_address, found_at) VALUES (?, ?, ?)",
                                     (priv, target_addr, time.time()))
                        print(f"[SKW HIT] *** PRIVATE KEY FOUND *** -> {priv} | Address: {target_addr}", flush=True)
            except Exception as e:
                print(f"[SKW Internal] Parser Error: {e}", flush=True)
        
        conn.close()
        print("[SKW Eng] Loop do motor finalizado.", flush=True)

def main():
    if not os.path.exists("kangaroo.exe"):
        print("[SKW Eng] ERROR: kangaroo.exe missing.")
        return
    eng = SKW_Engine()
    eng.start_search()

if __name__ == "__main__":
    main()
