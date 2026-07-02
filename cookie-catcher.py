#!/usr/bin/env python3
"""Grab Leonardo AI cookies from Chrome CDP and load into LeoStudio DB.

Usage:
  1. Log into Leonardo AI in Chrome
  2. Run: python3 cookie-catcher.py
  3. Open LeoStudio — cookies tab shows the account
"""
import json, struct, os, socket, base64, sqlite3, sys, time
from pathlib import Path

CDP_PORTS = [9226, 9228, 9227]  # headless Dimitri → headed login → visible Chrome
LEONARDO = "https://app.leonardo.ai"
DB = Path.home() / ".config" / "leostudio" / "app.db"


def cdp_tabs(port):
    import urllib.request
    try:
        return json.loads(urllib.request.urlopen(f"http://127.0.0.1:{port}/json/list", timeout=3).read())
    except:
        return []


def ws_extract_cookies(port, tab_id):
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(10)
    s.connect(("127.0.0.1", port))
    key = base64.b64encode(os.urandom(16)).decode()
    s.send(f"GET /devtools/page/{tab_id} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: {key}\r\nSec-WebSocket-Version: 13\r\n\r\n".encode())
    if b"101" not in s.recv(4096):
        s.close(); return []
    mid = 1
    pay = json.dumps({"id": mid, "method": "Network.getCookies", "params": {"urls": [LEONARDO]}}).encode()
    mask = os.urandom(4)
    fr = bytearray([0x81, 0x80 | len(pay)]); fr.extend(mask)
    for i, b in enumerate(pay): fr.append(b ^ mask[i % 4])
    s.send(bytes(fr))
    data = s.recv(65536); s.close()
    n = data[1] & 0x7F; o = 2
    if n == 126: n, o = struct.unpack(">H", data[2:4])[0], 4
    elif n == 127: n, o = struct.unpack(">Q", data[2:10])[0], 10
    m = data[o:o+4] if data[1] & 0x80 else None
    if m: o += 4
    p = data[o:o+n]
    if m: p = bytes(b ^ m[i % 4] for i, b in enumerate(p))
    return json.loads(p.decode()).get("result", {}).get("cookies", [])


def main():
    merged = {}
    for port in CDP_PORTS:
        tabs = cdp_tabs(port)
        leo = [t for t in tabs if "leonardo.ai" in t.get("url", "") and t["type"] == "page"]
        if not leo:
            continue
        print(f"  Port {port}: {len(leo)} Leonardo tab(s)")
        for t in leo:
            print(f"    {t['url'][:60]}")
            for c in ws_extract_cookies(port, t["id"]):
                name = c["name"]
                if name not in merged or c.get("httpOnly"):
                    merged[name] = c

    if not merged:
        print("No Leonardo tabs found. Open app.leonardo.ai in Chrome first."); sys.exit(1)

    cookie_str = "; ".join(f"{c['name']}={c['value']}" for c in merged.values())
    auth = [n for n in merged if "auth" in n.lower() or "session_data" in n.lower()]
    print(f"\n  {len(merged)} unique cookies, auth: {auth or 'NONE (not logged in?)'}")

    DB.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(str(DB))
    if conn.execute("SELECT 1 FROM cookies WHERE value=?", (cookie_str,)).fetchone():
        print("  Already in DB."); conn.close(); return
    conn.execute("INSERT INTO cookies (value, is_active, email, created_at) VALUES (?, 1, '', ?)",
                 (cookie_str, int(time.time())))
    conn.commit()
    total = conn.execute("SELECT count(*) FROM cookies").fetchone()[0]
    conn.close()
    print(f"  Loaded! Pool: {total} cookie(s)")


if __name__ == "__main__":
    main()
