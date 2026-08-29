#!/usr/bin/env bash
# One-command local smoke for the Go port. Sandboxed: fake HOME + ANYPICK_HOME,
# never touches your real ~/.codex or ~/.anypick.
#
#   ./scripts/smoke.sh
set -u
cd "$(dirname "$0")/.."

PASS=0; FAIL=0
ok()   { echo "  ✓ $1"; PASS=$((PASS+1)); }
bad()  { echo "  ✗ $1"; FAIL=$((FAIL+1)); }
check(){ if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (got: $2, want: $3)"; fi; }
contains(){ case "$2" in *"$3"*) ok "$1";; *) bad "$1 (missing: $3 in: $2)";; esac; }

echo "== build =="
BIN=$(mktemp -d)/anypick-go
if go build -o "$BIN" ./cmd/anypick 2>/dev/null; then ok "build"; else bad "build"; exit 1; fi

# Version must come from -ldflags injection (internal/buildinfo), never a
# hardcoded release number: a source build reports (devel) and the release
# pipeline links the single release tag.
BIN_VER=$(mktemp -d)/anypick-go-ver
if go build -ldflags "-X github.com/outof0/anypick/go/internal/buildinfo.Version=0.0.0-smoke" \
	-o "$BIN_VER" ./cmd/anypick 2>/dev/null; then
	contains "version ldflags injection" "$("$BIN_VER" --version 2>&1)" "0.0.0-smoke"
else
	bad "version-injected build"
fi
contains "source build reports devel" "$("$BIN" --version 2>&1)" "(devel)"

SB=$(mktemp -d)
mkdir -p "$SB/home/.codex"
printf '{"auth_mode":"chatgpt","tokens":{"id_token":"eyJhbGciOiJub25lIn0.eyJlbWFpbCI6InVzZXJAeC5jb20ifQ.sig","access_token":"tok","account_id":"a1"}}' > "$SB/home/.codex/auth.json"

run() { HOME="$SB/home" ANYPICK_HOME="$SB/data" ANYPICK_SECRET=sk-test \
        CODEX_AUTH_PATH= GROK_AUTH_PATH= OPENCODE_AUTH_PATH= \
        KIRO_SSO_CACHE_DIR= ANYPICK_KIRO_SECRET_DB= ANYPICK_CLAUDE_CREDENTIALS_PATH= \
        "$BIN" "$@" 2>&1; }

echo "== accounts & gateways =="
contains "add codex"            "$(run add codex --name work)"          "saved codex/work"
contains "gateway add openrouter" "$(run gateway add or --provider openrouter)" "https://openrouter.ai/api/v1"
contains "smart routing surface" "$(run gateway add or --provider openrouter 2>/dev/null)" "anthropic surface: https://openrouter.ai/api"

echo "== smart routing into Claude Code =="
run use claude --with or >/dev/null
BASE=$(python3 -c "import json;print(json.load(open('$SB/home/.claude/settings.json'))['env']['ANTHROPIC_BASE_URL'])")
check "ANTHROPIC_BASE_URL không có /v1" "$BASE" "https://openrouter.ai/api"

run use codex --with codex/work >/dev/null
check "native switch auth.json" "$(python3 -c "import json;print(json.load(open('$SB/home/.codex/auth.json'))['tokens']['access_token'])")" "tok"

echo "== proxy sống độc lập CLI =="
run add deepseek --name ds --kind api-key --secret sk-ds >/dev/null
OUT=$(run use claude --with deepseek/ds)
contains "activation qua managed proxy" "$OUT" "managed_builtin_proxy"
EP=$(echo "$OUT" | grep -o 'http://127\.0\.0\.1:[0-9]*' | head -1)
sleep 0.4
if curl -sf -m 2 "$EP/health" >/dev/null 2>&1; then ok "proxy còn sống sau khi CLI thoát"; else bad "proxy chết cùng CLI"; fi
ST=$(run proxy status deepseek ds --json)
contains "status cross-process" "$ST" '"running": true'
contains "status human by default (TS parity)" "$(run proxy status deepseek ds)" "Enabled:"
run proxy stop deepseek ds >/dev/null
if curl -sf -m 2 "$EP/health" >/dev/null 2>&1; then bad "stop không hiệu quả"; else ok "proxy stop dọn sạch"; fi

echo "== pool: kích hoạt qua pool:<provider> =="
run add deepseek --name dsb --kind api-key --secret sk-dsb >/dev/null
# Pools bind their planned/persisted port deterministically (pool.port ??
# provider default ?? 8080) and fail loudly on collision — pick a free one.
POOL_PORT=$(python3 -c "import socket;s=socket.socket();s.bind(('127.0.0.1',0));print(s.getsockname()[1]);s.close()")
contains "pool enable multi" "$(run proxy pool enable deepseek ds dsb --port "$POOL_PORT")" "multi/failover"
POOL_OUT=$(run use claude --with pool:deepseek)
contains "activation qua pool transport" "$POOL_OUT" "managed_builtin_proxy"
POOL_EP=$(ANYPICK_HOME="$SB/data" python3 -c "import json,sqlite3,os;d=os.environ['ANYPICK_HOME'];c=sqlite3.connect(d+'/anypick.db');r=c.execute(\"select state_json from proxy_state where provider='deepseek' and account='ds'\").fetchone();print(json.loads(r[0])['endpoint'])")
sleep 0.4
if curl -sf -m 2 "$POOL_EP/health" >/dev/null 2>&1; then ok "pool child còn sống sau khi CLI thoát"; else bad "pool child chết cùng CLI"; fi
CODE=$(curl -s -o /dev/null -w '%{http_code}' -m 2 -X POST "$POOL_EP/v1/chat/completions" -H 'Content-Type: application/json' -d '{}')
check "pool chặn thiếu bearer token" "$CODE" "401"
run proxy pool stop deepseek >/dev/null
sleep 1
if curl -sf -m 2 "$POOL_EP/health" >/dev/null 2>&1; then bad "pool stop không hiệu quả"; else ok "pool stop dọn sạch"; fi
LEAK=$(pgrep -f '_serve-pool' | wc -l | tr -d ' ')
check "không rò rỉ _serve-pool" "$LEAK" "0"

echo "== completion & __complete =="
COMP=$(run __complete clients)
contains "__complete clients liệt kê claude" "$COMP" "claude"
contains "completion bash shell out __complete" "$(run completion bash)" "__complete clients"
contains "completion zsh shell out __complete" "$(run completion zsh)" "__complete providers"
contains "completion fish shell out __complete" "$(run completion fish)" "__complete gateways"
if run definitely-not-a-command >/dev/null 2>&1; then bad "unknown command phải thất bại"; else ok "unknown command → usage + InvalidUsage"; fi

echo "== verbosity flags =="
Q=$(run --quiet use claude --with or)
[ -z "$Q" ] && ok "--quiet im lặng" || bad "--quiet vẫn in: $Q"
T_ERR=$(HOME="$SB/home" ANYPICK_HOME="$SB/data" "$BIN" --trace use claude --with or 2>&1 1>/dev/null)
contains "--trace ghi stderr" "$T_ERR" "[trace]"

echo "== rollback & recovery (suite đầy đủ) =="
if go test -race -timeout 300s ./... >/dev/null 2>&1; then ok "go test -race ./..."; else bad "go test -race ./..."; fi

rm -rf "$SB"
echo
echo "KẾT QUẢ: $PASS pass, $FAIL fail"
[ "$FAIL" -eq 0 ]
