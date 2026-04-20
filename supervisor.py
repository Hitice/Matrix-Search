import subprocess
import time
import os
import sqlite3
import json
from http.server import HTTPServer, BaseHTTPRequestHandler
import socketserver
import threading
from collections import deque

# -- Config ----------------------------------------------------
CONFIG_FILE = "current_target.json"
DB_NAME     = "state.db"
PORT        = 8080
AGENTS = [
    ["python", "-u", "brain_orchestrator.py"],
    ["python", "-u", "cuda_engine.py"]
]

# -- Shared state ----------------------------------------------
log_buffer    = deque(maxlen=300)
restart_event = threading.Event()
gpu_power     = "-"

# -- DB bootstrap ----------------------------------------------
def init_db():
    conn = sqlite3.connect(DB_NAME)
    conn.executescript("""
        CREATE TABLE IF NOT EXISTS agent_telemetry (
            agent_id TEXT PRIMARY KEY, status TEXT, speed REAL
        );
        CREATE TABLE IF NOT EXISTS distinguished_points (
            point_value TEXT PRIMARY KEY, kangaroo_type INTEGER, distance_value TEXT
        );
        CREATE TABLE IF NOT EXISTS search_state (
            target_pubkey TEXT PRIMARY KEY,
            last_start_hex TEXT,
            keys_scanned BIGINT,
            last_updated REAL
        );
        CREATE TABLE IF NOT EXISTS hits (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            private_key   TEXT UNIQUE,
            target_address TEXT,
            found_at      REAL
        );
    """)
    conn.commit()
    conn.close()

# -- Helpers ---------------------------------------------------
def db_write_hit(private_key, address):
    """Write a found key to the hits table. Uses WAL so readers don't block."""
    try:
        conn = sqlite3.connect(DB_NAME, timeout=10, check_same_thread=False)
        conn.execute("PRAGMA journal_mode=WAL")
        conn.execute(
            "INSERT OR IGNORE INTO hits (private_key, target_address, found_at) VALUES (?,?,?)",
            (private_key, address, time.time())
        )
        conn.commit()
        conn.close()
    except Exception as e:
        print(f"[SUP] DB write error: {e}")

def current_target_address():
    try:
        with open(CONFIG_FILE) as f:
            return json.load(f).get("target_address", "unknown")
    except:
        return "unknown"

# -- Log reader (runs in thread per subprocess) ----------------
def log_reader(pipe, name):
    try:
        with pipe:
            for raw in iter(pipe.readline, ''):
                line = raw.strip()
                if not line:
                    continue
                ts = time.strftime('%H:%M:%S')

                # Detect HIT lines -- check for the tag cuda_engine.py prints
                is_hit = (
                    "[SKW HIT]" in line or
                    "PRIVATE KEY FOUND" in line or
                    line.upper().startswith("HIT:")
                )

                if is_hit:
                    priv = None
                    addr = current_target_address()

                    # Format: [SKW HIT] *** PRIVATE KEY FOUND *** -> <hex> | Address: <addr>
                    if "->" in line:
                        try:
                            after_arrow = line.split("->")[1].strip()
                            parts = after_arrow.split("|")
                            priv = parts[0].strip()
                            if len(parts) > 1 and "Address:" in parts[1]:
                                addr = parts[1].split("Address:")[1].strip()
                        except Exception as pe:
                            log_buffer.append(f"[{ts}] [SUP] HIT parse error: {pe}")
                    # Fallback: HIT:<hex>\n
                    if not priv and ":" in line:
                        try:
                            priv = line.split(":", 1)[1].strip().split()[0]
                        except:
                            pass

                    if priv:
                        db_write_hit(priv, addr)
                        log_buffer.append(f"[{ts}] [HIT] *** PRIVATE KEY FOUND: {priv}")
                        log_buffer.append(f"[{ts}] [HIT] Address: {addr}")
                        
                        # Auto-halt the engine
                        try:
                            if os.path.exists(CONFIG_FILE):
                                with open(CONFIG_FILE) as f: cfg = json.load(f)
                                cfg['status'] = 'STOPPED'
                                with open(CONFIG_FILE, 'w') as f: json.dump(cfg, f, indent=2)
                            os.system("taskkill /F /IM kangaroo.exe /T 2>nul")
                            log_buffer.append(f"[{ts}] [SYS] Engine automatically halted due to HIT.")
                            restart_event.set()
                        except: pass
                    else:
                        log_buffer.append(f"[{ts}] [{name}] {line}")
                else:
                    log_buffer.append(f"[{ts}] [{name}] {line}")
    except Exception as e:
        log_buffer.append(f"[{time.strftime('%H:%M:%S')}] [SUP] log_reader error: {e}")

# -- GPU power monitor -----------------------------------------
def gpu_power_monitor():
    global gpu_power
    while True:
        try:
            res = subprocess.check_output(
                "nvidia-smi --query-gpu=power.draw --format=csv,noheader,nounits",
                shell=True, timeout=3
            ).decode().strip()
            gpu_power = f"{res}W"
        except:
            pass
        time.sleep(3)

# -- HTTP API --------------------------------------------------
class SKW_API(BaseHTTPRequestHandler):

    def do_GET(self):
        if self.path == '/':
            self._serve_file('dashboard.html', 'text/html')
        elif self.path == '/api/stats':
            self._serve_stats()
        else:
            self.send_error(404)

    def _serve_file(self, path, content_type):
        try:
            with open(path, 'rb') as f:
                data = f.read()
            self.send_response(200)
            self.send_header('Content-type', content_type)
            self.send_header('Content-Length', len(data))
            self.end_headers()
            self.wfile.write(data)
        except:
            self.send_error(500)

    def _serve_stats(self):
        stats = {
            "config":        {},
            "speed":         0,
            "total_scanned": 0,
            "logs":          list(log_buffer),
            "power":         gpu_power,
            "hits":          [],
        }
        try:
            if os.path.exists(CONFIG_FILE):
                with open(CONFIG_FILE) as f:
                    stats["config"] = json.load(f)
            
            pubkey = stats["config"].get("target_public_key")

            # Independent read-only connection -- no locking needed with WAL
            conn   = sqlite3.connect(DB_NAME, timeout=3, check_same_thread=False)
            conn.execute("PRAGMA journal_mode=WAL")
            cursor = conn.cursor()
            
            # 1. Total Speed
            cursor.execute("SELECT speed FROM agent_telemetry WHERE agent_id='GPU_WORKER'")
            row = cursor.fetchone()
            if row: stats["speed"] = row[0]
            
            # 2. Real Scanned Progress (from persistence table)
            if pubkey:
                cursor.execute("SELECT keys_scanned FROM search_state WHERE target_pubkey = ?", (pubkey,))
                state_row = cursor.fetchone()
                if state_row: stats["total_scanned"] = state_row[0]

            # 3. Hits
            cursor.execute(
                "SELECT private_key, target_address, found_at FROM hits ORDER BY found_at DESC LIMIT 20"
            )
            stats["hits"] = [
                {"private_key": r[0], "address": r[1], "found_at": r[2]}
                for r in cursor.fetchall()
            ]
            conn.close()
        except Exception as e:
            stats["_err"] = str(e)

        body = json.dumps(stats).encode()
        self.send_response(200)
        self.send_header('Content-type', 'application/json')
        self.send_header('Content-Length', len(body))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length   = int(self.headers.get('Content-Length', 0))
        raw_body = self.rfile.read(length)

        if self.path == '/api/config':
            try:
                cfg = json.loads(raw_body)
                cfg['status'] = 'RUNNING'
                with open(CONFIG_FILE, 'w') as f:
                    json.dump(cfg, f, indent=2)
                log_buffer.clear() # ALWAYS clear old history before starting a new one
                log_buffer.append(
                    f"[{time.strftime('%H:%M:%S')}] [SYS] Novo alvo implantado: {cfg.get('target_address','?')}"
                )
                restart_event.set()
                self.send_response(200); self.end_headers()
            except Exception as e:
                self.send_response(400); self.end_headers()
                self.wfile.write(str(e).encode())

        elif self.path == '/api/clear_logs':
            log_buffer.clear()
            self.send_response(200); self.end_headers()

        elif self.path == '/api/terminate':
            try:
                if os.path.exists(CONFIG_FILE):
                    with open(CONFIG_FILE) as f: cfg = json.load(f)
                    cfg['status'] = 'STOPPED'
                    with open(CONFIG_FILE, 'w') as f: json.dump(cfg, f, indent=2)
                os.system("taskkill /F /IM kangaroo.exe /T 2>nul")
                log_buffer.append(f"[{time.strftime('%H:%M:%S')}] [SYS] Worker finalizado pelo usuário.")
                restart_event.set()
                self.send_response(200); self.end_headers()
            except Exception as e:
                self.send_response(500); self.end_headers()
        else:
            self.send_error(404)

    def log_message(self, *args): pass  # silence HTTP access log

# -- Main loop -------------------------------------------------
def main():
    init_db()
    print("--- SECP256 KEY WORKER: SUPERVISOR v4.0 ---")

    threading.Thread(target=gpu_power_monitor, daemon=True).start()

    class ThreadedHTTPServer(socketserver.ThreadingMixIn, HTTPServer):
        allow_reuse_address = True
        daemon_threads = True

    server = ThreadedHTTPServer(('0.0.0.0', PORT), SKW_API)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    print(f"[SUP] Dashboard -> http://localhost:{PORT}")

    processes = []
    while True:
        if restart_event.is_set() or not processes:
            restart_event.clear()

            # Kill everything
            os.system("taskkill /F /IM kangaroo.exe /T 2>nul")
            for p in processes:
                try: p.terminate()
                except: pass
            processes = []
            time.sleep(0.5)

            # Restart agents if RUNNING
            try:
                if os.path.exists(CONFIG_FILE):
                    with open(CONFIG_FILE) as f: cfg = json.load(f)
                    if cfg.get('status') == 'RUNNING':
                        for cmd in AGENTS:
                            p = subprocess.Popen(
                                cmd,
                                stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT,
                                text=True,
                                bufsize=1
                            )
                            threading.Thread(
                                target=log_reader,
                                args=(p.stdout, cmd[-1]),
                                daemon=True
                            ).start()
                            processes.append(p)
                        log_buffer.append(f"[{time.strftime('%H:%M:%S')}] [SYS] Swarm iniciado ({len(AGENTS)} agentes).")
            except Exception as e:
                print(f"[SUP] Start error: {e}")

        time.sleep(1.5)

if __name__ == "__main__":
    main()
