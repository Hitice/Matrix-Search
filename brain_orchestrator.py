import sqlite3
import time
import os
import json

# Configuration
DB_NAME = "state.db"
CONFIG_FILE = "current_target.json"

def load_config():
    with open(CONFIG_FILE, 'r') as f:
        return json.load(f)

def check_for_collision(target_x):
    conn = sqlite3.connect(DB_NAME)
    cursor = conn.cursor()
    cursor.execute("SELECT distance_value FROM distinguished_points WHERE point_value = ?", (hex(target_x),))
    hit = cursor.fetchone()
    conn.close()
    return hit

def main():
    config = load_config()
    target_x = int(config['target_public_key'][2:], 16)
    
    print(f"--- SECP256 KEY WORKER: CÉREBRO ATIVO ---")
    print(f"Alvo: {config['target_address']}")
    
    while True:
        hit = check_for_collision(target_x)
        if hit:
            print(f"\n[HIT] Colisão Encontrada! Dados: {hit[0]}")
            with open("hit.txt", "a") as f:
                f.write(f"Puzzle #{config['target_puzzle']} HIT: {hit[0]}\n")
            break
        time.sleep(30)

if __name__ == "__main__":
    main()
