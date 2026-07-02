#!/usr/bin/env python3
"""Tiny server that receives cookie strings from the Chrome extension and writes to LeoStudio DB.

Also exposes /auto-refresh which grabs fresh cookies from Chrome CDP
without requiring the extension click — used by the Go backend's
background refresh goroutine as a fallback when better-auth sessions expire.

Usage:
  python3 import_server.py          # default port 8001
  python3 import_server.py 9000     # custom port

Then install chrome-extension/ in Chrome and click Export.
"""
import json, sqlite3, sys, time
from http.server import HTTPServer, BaseHTTPRequestHandler
from pathlib import Path

DB = Path.home() / ".config" / "leostudio" / "app.db"
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 8001

# ── CDP cookie extraction (inline, no deps) ─────────────────────────────────

CDP_PORTS = [9226, 9228, 9227]  # headless Dimitri → headed login → visible Chrome
LEONARDO = "https://app.leonardo.ai"


def _cdp_tabs(port):
    import urllib.request
    try:
        return json.loads(urllib.request.urlopen(
            f"http://127.0.0.1:{port}/json/list", timeout=3
        ).read())
    except Exception:
        return []


def _ws_extract_cookies(port, tab_id):
    import struct, os, socket, base64
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(10)
    s.connect(("127.0.0.1", port))
    key = base64.b64encode(os.urandom(16)).decode()
    s.send(
        f"GET /devtools/page/{tab_id} HTTP/1.1\r\n"
        f"Host: 127.0.0.1:{port}\r\n"
        f"Upgrade: websocket\r\n"
        f"Connection: Upgrade\r\n"
        f"Sec-WebSocket-Key: {key}\r\n"
        f"Sec-WebSocket-Version: 13\r\n\r\n".encode()
    )
    if b"101" not in s.recv(4096):
        s.close()
        return []
    mid = 1
    pay = json.dumps({
        "id": mid,
        "method": "Network.getCookies",
        "params": {"urls": [LEONARDO]},
    }).encode()
    mask = os.urandom(4)
    fr = bytearray([0x81, 0x80 | len(pay)])
    fr.extend(mask)
    for i, b in enumerate(pay):
        fr.append(b ^ mask[i % 4])
    s.send(bytes(fr))
    data = s.recv(65536)
    s.close()
    n = data[1] & 0x7F
    o = 2
    if n == 126:
        n, o = struct.unpack(">H", data[2:4])[0], 4
    elif n == 127:
        n, o = struct.unpack(">Q", data[2:10])[0], 10
    m = data[o:o + 4] if data[1] & 0x80 else None
    if m:
        o += 4
    p = data[o:o + n]
    if m:
        p = bytes(b ^ m[i % 4] for i, b in enumerate(p))
    return json.loads(p.decode()).get("result", {}).get("cookies", [])


def _grab_cookies_from_cdp():
    """Extract Leonardo cookies from Chrome CDP across known ports.

    Returns (cookie_str, auth_names) or (None, []) on failure.
    """
    merged = {}
    for port in CDP_PORTS:
        tabs = _cdp_tabs(port)
        leo = [
            t for t in tabs
            if "leonardo.ai" in t.get("url", "") and t["type"] == "page"
        ]
        if not leo:
            continue
        for t in leo:
            for c in _ws_extract_cookies(port, t["id"]):
                name = c["name"]
                if name not in merged or c.get("httpOnly"):
                    merged[name] = c

    if not merged:
        return None, []

    cookie_str = "; ".join(f"{c['name']}={c['value']}" for c in merged.values())
    auth = [n for n in merged if "auth" in n.lower() or "session_data" in n.lower()]
    return cookie_str, auth


def _upsert_cookie(conn, cookie_str):
    """Insert or update a cookie in the DB. Returns (new, total)."""
    exists = conn.execute(
        "SELECT 1 FROM cookies WHERE value=?", (cookie_str,)
    ).fetchone()
    if exists:
        # Already exists — touch last_checked_at so we know it was re-verified.
        conn.execute(
            "UPDATE cookies SET last_checked_at = ? WHERE value = ?",
            (int(time.time()), cookie_str),
        )
        conn.commit()
        total = conn.execute("SELECT count(*) FROM cookies").fetchone()[0]
        return False, total

    conn.execute(
        "INSERT INTO cookies (value, is_active, email, created_at) VALUES (?, 1, '', ?)",
        (cookie_str, int(time.time())),
    )
    conn.commit()
    total = conn.execute("SELECT count(*) FROM cookies").fetchone()[0]
    return True, total


# ── HTTP handler ─────────────────────────────────────────────────────────────

class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path == "/import":
            self._import_cookie()
        else:
            self.send_error(404)

    def do_GET(self):
        if self.path == "/health":
            self._json(200, {"ok": True})
        elif self.path == "/auto-refresh":
            self._auto_refresh()
        else:
            self.send_error(404)

    def _import_cookie(self):
        """Receive a cookie string from the Chrome extension."""
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode()
        if not body.strip():
            self._json(400, {"ok": False, "error": "empty cookie string"})
            return

        conn = sqlite3.connect(str(DB))
        try:
            new, total = _upsert_cookie(conn, body)
            self._json(200, {"ok": True, "total": total, "new": new})
        except Exception as e:
            self._json(500, {"ok": False, "error": str(e)})
        finally:
            conn.close()

    def _auto_refresh(self):
        """Grab fresh cookies from Chrome CDP and write to DB.

        Called by the Go backend's background refresh goroutine as a fallback
        when better-auth session tokens expire. Requires Chrome to be running
        with an active app.leonardo.ai session.
        """
        cookie_str, auth = _grab_cookies_from_cdp()
        if cookie_str is None:
            self._json(200, {"ok": False, "error": "no Leonardo tabs in Chrome"})
            return

        conn = sqlite3.connect(str(DB))
        try:
            new, total = _upsert_cookie(conn, cookie_str)
            self._json(200, {
                "ok": True,
                "total": total,
                "new": new,
                "auth_cookies": auth,
            })
        except Exception as e:
            self._json(500, {"ok": False, "error": str(e)})
        finally:
            conn.close()

    def _json(self, code, data):
        body = json.dumps(data).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", len(body))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.end_headers()
        self.wfile.write(body)

    def do_OPTIONS(self):
        self.send_response(204)
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.end_headers()

    def log_message(self, fmt, *args):
        print(f"[import] {args[0]}")


if __name__ == "__main__":
    print(f"LeoStudio import server on http://127.0.0.1:{PORT}")
    print(f"  POST /import       — receive cookies from Chrome extension")
    print(f"  GET  /auto-refresh  — grab cookies from Chrome CDP (no extension needed)")
    print(f"  GET  /health        — health check")
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
