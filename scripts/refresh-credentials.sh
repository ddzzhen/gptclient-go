#!/usr/bin/env bash
set -euo pipefail

# refresh-credentials.sh — Extract ChatGPT credentials from Playwright MCP browser,
#                          write tokens.json + browser_session.json, deploy to VPS, reload container.
#
# Usage:
#   ./scripts/refresh-credentials.sh            # extract + deploy + reload
#   ./scripts/refresh-credentials.sh extract   # only extract from browser
#   ./scripts/refresh-credentials.sh deploy    # only scp to VPS
#   ./scripts/refresh-credentials.sh reload     # only restart container
#   SKIP_DEPLOY=1 ./scripts/refresh-credentials.sh  # extract only, no VPS transfer

# ─── Configuration ───────────────────────────────────────────────────────────
MCP_URL="${MCP_URL:-http://localhost:8931/mcp}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
TOKENS_FILE="${TOKENS_FILE:-$PROJECT_DIR/tokens.json}"
SESSION_FILE="${SESSION_FILE:-$PROJECT_DIR/data/browser_session.json}"

VPS_HOST="${VPS_HOST:-root@ge4.whatsknow.com}"
VPS_PORT="${VPS_PORT:-29026}"
VPS_DIR="${VPS_DIR:-/home/apps/gptclient-go}"
VPS_CONTAINER="${VPS_CONTAINER:-sentinel-go}"

# ─── Helpers ────────────────────────────────────────────────────────────────
log()  { printf '\033[34m[%s]\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
okk()  { printf '\033[32m[%s] OK\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; }
fail() { printf '\033[31m[%s] FAIL\033[0m %s\n' "$(date +%H:%M:%S)" "$*"; exit 1; }

# ─── Step 1: Extract ─────────────────────────────────────────────────────────
extract() {
    log "Extracting credentials from Playwright MCP ($MCP_URL)..."

    [ ! -f "$TOKENS_FILE" ] && fail "tokens.json not found — run from the project root."
    mkdir -p "$(dirname "$SESSION_FILE")"

    # All MCP communication + extraction in one Python block
    python3 - "$MCP_URL" "$TOKENS_FILE" "$SESSION_FILE" <<'PYEXTRACT'
import json, sys, base64, datetime, requests

MCP_URL = sys.argv[1]
TOKENS_FILE = sys.argv[2]
SESSION_FILE = sys.argv[3]

HEADERS = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
sess = requests.Session()
_id = [0]
def nid():
    _id[0] += 1
    return _id[0]

def mcp_call(method, args=None, timeout=60):
    r = sess.post(MCP_URL, headers=HEADERS, json={
        "jsonrpc": "2.0", "method": "tools/call", "id": nid(),
        "params": {"name": method, "arguments": args or {}},
    }, stream=True, timeout=timeout)
    text = ""
    for chunk in r.iter_content(chunk_size=None):
        if chunk:
            text += chunk.decode("utf-8", errors="replace")
    for line in text.split("\n"):
        if line.startswith("data: "):
            return json.loads(line[6:])
    return None

def get_result_text(result):
    if not result:
        return ""
    content = result.get("result", {}).get("content", [])
    return content[0].get("text", "") if content else ""

def parse_eval(text):
    """Parse the double-encoded string from browser_evaluate result."""
    lines = text.split("\n")
    for i, line in enumerate(lines):
        if line.strip() == "### Result":
            raw = lines[i + 1].strip() if i + 1 < len(lines) else ""
            try:
                return json.loads(json.loads(raw))
            except:
                try:
                    return json.loads(raw)
                except:
                    return None
    return None

# ── Initialize MCP session ──
r = sess.post(MCP_URL, headers=HEADERS, json={
    "jsonrpc": "2.0", "method": "initialize", "id": nid(),
    "params": {"protocolVersion": "2024-11-05", "capabilities": {},
               "clientInfo": {"name": "refresh-creds", "version": "1.0"}},
}, timeout=10)
sid = r.headers.get("mcp-session-id", "")
if not sid:
    print("FAIL: Cannot get MCP session ID. Is Chrome with Playwright MCP running on " + MCP_URL + "?")
    sys.exit(1)
HEADERS["Mcp-Session-Id"] = sid
sess.post(MCP_URL, headers=HEADERS, json={"jsonrpc": "2.0", "method": "notifications/initialized"}, timeout=10)

# ── Navigate to chatgpt.com ──
print("  Navigating to chatgpt.com...")
mcp_call("browser_navigate", {"url": "https://chatgpt.com"})
import time; time.sleep(2)

# ── Extract session via /api/auth/session ──
print("  Fetching /api/auth/session...")
result = mcp_call("browser_evaluate", {
    "function": "() => fetch('/api/auth/session').then(r => r.json()).then(d => JSON.stringify({at: d.accessToken, st: d.sessionToken, exp: d.expires, user: d.user?.email || 'unknown'}))"
})
session_data = parse_eval(get_result_text(result))
if not session_data or not session_data.get("at"):
    print("FAIL: Not logged in. Please log in to ChatGPT in the Chrome browser first, then re-run.")
    sys.exit(1)

at = session_data["at"]
st = session_data.get("st", "")
user = session_data.get("user", "")

# ── Extract cookies ──
print("  Extracting cookies...")
result2 = mcp_call("browser_evaluate", {"function": "() => document.cookie"})
cookie_str = parse_eval(get_result_text(result2)) or ""

# ── Extract UA and device ID ──
print("  Extracting device info...")
result3 = mcp_call("browser_evaluate", {
    "function": "() => JSON.stringify({ua: navigator.userAgent, did: localStorage.getItem('oai-did') || ''})"
})
info = parse_eval(get_result_text(result3)) or {}
user_agent = info.get("ua", "")
device_id = info.get("did", "")

# ── Parse JWT exp ──
jwt_exp = ""
try:
    payload = at.split(".")[1]
    payload += "=" * (4 - len(payload) % 4)
    claims = json.loads(base64.b64decode(payload))
    jwt_exp = datetime.datetime.fromtimestamp(claims["exp"]).strftime("%Y-%m-%dT%H:%M:%S+08:00")
except:
    pass

# ── Summary ──
print(f"  User:       {user}")
print(f"  AT length:  {len(at)} chars, prefix: {at[:40]}...")
print(f"  ST length:  {len(st)} chars")
print(f"  Device ID:  {device_id}")
print(f"  UA:         {user_agent[:60]}...")
print(f"  Cookie:     {len(cookie_str)} chars")
print(f"  AT expires: {jwt_exp}")

if not st:
    print("FAIL: Session token is empty — cannot auto-refresh in the future.")
    sys.exit(1)

# ── Write tokens.json ──
tokens = {
    "version": 1,
    "tokens": [{
        "id": "browser-mcp",
        "access_token": at,
        "session_token": st,
        "expires_at": jwt_exp,
        "updated_at": datetime.datetime.now().strftime("%Y-%m-%dT%H:%M:%S+08:00"),
    }]
}
with open(TOKENS_FILE, "w") as f:
    json.dump(tokens, f, indent=2)
print(f"  Written: {TOKENS_FILE}")

# ── Write browser_session.json ──
browser_session = {
    "access_token": at,
    "session_token": st,
    "cookie_string": cookie_str,
    "device_id": device_id,
    "user_agent": user_agent,
    "expires": jwt_exp,
}
with open(SESSION_FILE, "w") as f:
    json.dump(browser_session, f, indent=2)
print(f"  Written: {SESSION_FILE}")

print("EXTRACT_OK")
PYEXTRACT

    [ $? -ne 0 ] && fail "Extraction failed."
    okk "Extraction complete."
}

# ─── Step 2: Deploy ──────────────────────────────────────────────────────────
deploy() {
    log "Deploying to VPS ($VPS_HOST:$VPS_PORT)..."

    [ ! -f "$TOKENS_FILE" ] && fail "$TOKENS_FILE not found. Run 'extract' first."
    [ ! -f "$SESSION_FILE" ] && fail "$SESSION_FILE not found. Run 'extract' first."

    log "  Testing SSH..."
    ssh -o ConnectTimeout=5 -o StrictHostKeyChecking=accept-new -p "$VPS_PORT" "$VPS_HOST" "echo ok" >/dev/null 2>&1 \
        || fail "SSH connection failed to $VPS_HOST:$VPS_PORT"

    log "  Uploading tokens.json..."
    scp -P "$VPS_PORT" "$TOKENS_FILE" "$VPS_HOST:$VPS_DIR/tokens.json"

    log "  Uploading browser_session.json..."
    ssh -o StrictHostKeyChecking=accept-new -p "$VPS_PORT" "$VPS_HOST" "mkdir -p $VPS_DIR/data"
    scp -P "$VPS_PORT" "$SESSION_FILE" "$VPS_HOST:$VPS_DIR/data/browser_session.json"

    log "  Verifying..."
    ssh -o StrictHostKeyChecking=accept-new -p "$VPS_PORT" "$VPS_HOST" "python3 -c \"
import json
t = json.load(open('$VPS_DIR/tokens.json'))
print('  tokens:', len(t.get('tokens',[])))
tk = t['tokens'][0] if t.get('tokens') else {}
print('  AT:', tk.get('access_token','')[:40] + '...')
bs = json.load(open('$VPS_DIR/data/browser_session.json'))
print('  device_id:', bs.get('device_id',''))
print('  cookie len:', len(bs.get('cookie_string','')))
\""

    okk "Deploy complete."
}

# ─── Step 3: Reload ──────────────────────────────────────────────────────────
reload() {
    log "Restarting container $VPS_CONTAINER on VPS..."

    ssh -o ConnectTimeout=5 -p "$VPS_PORT" "$VPS_HOST" "
        cd $VPS_DIR
        docker restart $VPS_CONTAINER
        sleep 3
        echo '=== logs ==='
        docker logs $VPS_CONTAINER 2>&1 | tail -5
        echo '=== health ==='
        curl -sS --max-time 5 http://localhost:5006/health 2>/dev/null || echo 'health check failed'
    "

    okk "Reload complete."
}

# ─── Main ────────────────────────────────────────────────────────────────────
main() {
    local step="${1:-all}"
    case "$step" in
        extract) extract ;;
        deploy)  deploy ;;
        reload)  reload ;;
        all)
            extract
            echo ""
            if [ "${SKIP_DEPLOY:-}" = "1" ]; then
                log "SKIP_DEPLOY=1 — skipping VPS transfer."
            else
                deploy
                echo ""
                reload
            fi
            ;;
        -h|--help|help)
            sed -n '2,12p' "$0"
            ;;
        *)
            fail "Unknown step: $step (use: extract, deploy, reload, all)"
            ;;
    esac
    echo ""
    okk "Done!"
}

main "$@"
