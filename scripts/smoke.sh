#!/usr/bin/env bash
#
# a2a-tui live-agent smoke test.
#
# Verifies a fixed table of public A2A agents in two phases per agent:
#
#   [1/2] card  GET /.well-known/agent-card.json (v1.0 path), falling back to
#               /.well-known/agent.json (v0.3 path). Expects HTTP 200 and a
#               JSON body; extracts name and protocolVersion.
#   [2/2] send  POSTs exactly one benign JSON-RPC request asking the agent to
#               reply "pong" — nothing else. Dialect follows the card:
#                 v1.0: method SendMessage, role ROLE_USER, text part,
#                       A2A-Version: 1.0 header, POST target = the card's
#                       url / supportedInterfaces[].url (fallback: origin).
#                 v0.3: method message/send, role user, {kind:text} part,
#                       no A2A-Version header, same target resolution.
#
# Verdicts per agent: PASS (both phases), SKIP (auth/payment required and no
# token supplied, or an auth-flagged agent without a token), FAIL (anything
# else: unreachable, non-200, non-JSON, JSON-RPC error object, empty body).
#
# Usage:
#   scripts/smoke.sh            # exit 0 if >= 1 agent passed both phases
#   scripts/smoke.sh --strict   # additionally exit nonzero if any
#                               # non-skipped agent failed
#
# Environment:
#   SMOKE_TOKEN_<NAME>          # per-agent bearer token, appended as
#                               # "Authorization: Bearer <token>". NAME is the
#                               # agent name from the table below, upper-cased
#                               # with "-" -> "_". Auth-flagged agents skip the
#                               # send phase unless their token is set.
#                               #   e.g. SMOKE_TOKEN_AGENT_READY, SMOKE_TOKEN_HUMANBROWSER
#
# Dependencies: curl + python3 (jq is not needed). Public agents drift
# without notice; failures are expected and informational — see
# docs/live-agents.md for the last-verified status of each agent.

set -euo pipefail

CARD_TIMEOUT=10
SEND_TIMEOUT=20

# name|base-url|auth-required(yes/no)
AGENTS=(
  "moneyyoureowed|https://agent.moneyyoureowed.com|no"
  "ideatrace|https://ideatrace-a2a-server.vercel.app|no"
  "agent-ready|https://agent-ready.dev|yes"
  "agentopt|https://agentopt.app|yes"
  "thehiveryiq|https://thehiveryiq.com|no"
  "rosentic|https://api.rosentic.com|no"
  "humanbrowser|https://agent.humanbrowser.cloud|yes"
)

STRICT=0
if [[ "${1:-}" == "--strict" ]]; then
  STRICT=1
elif [[ $# -gt 0 ]]; then
  echo "usage: $0 [--strict]" >&2
  exit 2
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

pass=0
fail=0
skip=0
cards_ok=0
summary=()   # one PASS/FAIL/SKIP line per agent, printed at the end

# card_field FILE -> "name<TAB>proto<TAB>endpoint<TAB>streaming<TAB>push"
# Parses a fetched card; exits nonzero (with a message on stdout) if the body
# is not JSON.
card_field() {
  python3 - "$1" <<'PY'
import json, sys
try:
    c = json.load(open(sys.argv[1], encoding="utf-8"))
    if not isinstance(c, dict):
        raise ValueError("card is not a JSON object")
except Exception as e:
    print(f"not a JSON card: {e}")
    sys.exit(1)
name = c.get("name") or "(unnamed)"
proto = str(c.get("protocolVersion") or "")
endpoint = c.get("url") or ""
for i in c.get("supportedInterfaces") or []:
    if isinstance(i, dict) and i.get("url"):
        endpoint = endpoint or i["url"]
        break
caps = c.get("capabilities") or {}
streaming = "yes" if caps.get("streaming") else "no"
push = "yes" if caps.get("pushNotifications") else "no"
print(f"{name}\t{proto}\t{endpoint}\t{streaming}\t{push}")
PY
}

# send_verdict FILE -> "PASS<TAB>summary" | "FAIL<TAB>reason" (120-char cap on
# the summary). Accepts any 2xx body carrying a JSON-RPC result (task or
# message shape); a JSON-RPC error object is a FAIL.
send_verdict() {
  python3 - "$1" <<'PY'
import json, sys
raw = open(sys.argv[1], "rb").read()
if not raw.strip():
    print("FAIL\tempty response body")
    sys.exit()
try:
    r = json.loads(raw)
except Exception as e:
    print(f"FAIL\tnon-JSON body ({str(e)[:50]})")
    sys.exit()
if not isinstance(r, dict):
    print("FAIL\tbody is not a JSON-RPC response")
    sys.exit()
err = r.get("error")
if err:
    code = err.get("code") if isinstance(err, dict) else err
    msg = (err.get("message") if isinstance(err, dict) else "") or ""
    print(f"FAIL\tJSON-RPC error {code}: {str(msg)[:80]}")
    sys.exit()
res = r.get("result")
if res is None:
    print("FAIL\tresponse has neither result nor error")
    sys.exit()

def summarize(res):
    if isinstance(res, dict):
        parts = None
        if isinstance(res.get("message"), dict):
            parts = res["message"].get("parts")   # wrapped message result
        if parts is None:
            parts = res.get("parts")              # bare message result (0.3)
        if isinstance(parts, list) and parts:
            out = []
            for p in parts:
                if not isinstance(p, dict):
                    continue
                if p.get("text"):
                    out.append(str(p["text"]))
                elif "data" in p:
                    out.append(json.dumps(p["data"], ensure_ascii=False))
            if out:
                return " ".join(out)
        status = res.get("status")
        if isinstance(status, dict) and status.get("state"):
            tid = res.get("id") or res.get("taskId") or "?"
            return f"task {tid} state={status['state']}"
    return json.dumps(res, ensure_ascii=False)

print("PASS\t" + summarize(res).replace("\n", " ")[:120])
PY
}

echo "a2a-tui live-agent smoke — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

idx=0
for row in "${AGENTS[@]}"; do
  IFS='|' read -r name url auth <<<"$row"
  idx=$((idx + 1))
  echo "== [$idx/${#AGENTS[@]}] $name ($url)"

  # ---- Phase 1: card -----------------------------------------------------
  card=""
  card_err=""
  for path in "/.well-known/agent-card.json" "/.well-known/agent.json"; do
    : >"$tmp/body"
    rc=0
    code=$(curl -sS -L -m "$CARD_TIMEOUT" -o "$tmp/body" -w '%{http_code}' \
      "$url$path" 2>"$tmp/err") || rc=$?
    if [[ $rc -ne 0 ]]; then
      card_err="curl error ${rc}: $(tr '\n' ' ' <"$tmp/err" | cut -c1-80)"
      continue
    fi
    if [[ "$code" != "200" ]]; then
      card_err="http $code for $path"
      continue
    fi
    if out=$(card_field "$tmp/body"); then
      card="$out"
      break
    fi
    card_err="bad card body at $path: $out"
  done

  if [[ -z "$card" ]]; then
    echo "[1/2] card  FAIL ($card_err)"
    echo "[2/2] send  SKIP (no card)"
    fail=$((fail + 1))
    summary+=("FAIL $name — card: $card_err")
    echo
    continue
  fi

  IFS=$'\t' read -r card_name proto endpoint streaming push <<<"$card"
  cards_ok=$((cards_ok + 1))
  echo "[1/2] card  OK name=\"$card_name\" proto=${proto:-?} streaming=$streaming push=$push"

  # ---- Phase 2: one benign send -------------------------------------------
  tokvar="SMOKE_TOKEN_$(echo "$name" | tr '[:lower:]-' '[:upper:]_')"
  token="${!tokvar:-}"

  if [[ "$auth" == "yes" && -z "$token" ]]; then
    echo "[2/2] send  SKIP (auth required; set $tokvar)"
    skip=$((skip + 1))
    summary+=("SKIP $name — send: auth required, $tokvar not set")
    echo
    continue
  fi

  # Dialect from the card's protocolVersion (missing -> assume v1.0).
  if [[ "$proto" == 1* ]]; then
    method="SendMessage"
    role="ROLE_USER"
    part="{\"text\":\"Reply with the single word: pong.\"}"
    extra=(-H "A2A-Version: 1.0")
  else
    method="message/send"
    role="user"
    part="{\"kind\":\"text\",\"text\":\"Reply with the single word: pong.\"}"
    extra=()
  fi

  target="$endpoint"
  [[ -n "$target" ]] || target="$url"
  mid="smoke-$(date +%s)-$idx"
  payload=$(printf '{"jsonrpc":"2.0","id":1,"method":"%s","params":{"message":{"messageId":"%s","role":"%s","parts":[%s]}}}' \
    "$method" "$mid" "$role" "$part")

  hdrs=(-H "Content-Type: application/json" "${extra[@]}")
  [[ -n "$token" ]] && hdrs+=(-H "Authorization: Bearer $token")

  rc=0
  code=$(curl -sS -m "$SEND_TIMEOUT" -o "$tmp/body" -w '%{http_code}' \
    -X POST "$target" "${hdrs[@]}" -d "$payload" 2>"$tmp/err") || rc=$?

  if [[ $rc -ne 0 ]]; then
    reason="curl error ${rc}: $(tr '\n' ' ' <"$tmp/err" | cut -c1-80)"
  elif [[ "$code" == 401 || "$code" == 402 || "$code" == 403 ]]; then
    echo "[2/2] send  SKIP (http $code: auth/payment required)"
    skip=$((skip + 1))
    summary+=("SKIP $name — send: http $code auth/payment required${token:+despite $tokvar}")
    echo
    continue
  elif [[ "$code" != 2* ]]; then
    reason="http $code from POST $target"
  else
    verdict=$(send_verdict "$tmp/body")
    IFS=$'\t' read -r v detail <<<"$verdict"
    if [[ "$v" == "PASS" ]]; then
      echo "[2/2] send  OK reply=\"$detail\""
      pass=$((pass + 1))
      summary+=("PASS $name — card ok ($proto), send ok")
      echo
      continue
    fi
    reason="$detail"
  fi

  echo "[2/2] send  FAIL ($reason)"
  fail=$((fail + 1))
  summary+=("FAIL $name — send: $reason")
  echo
done

# ---- Summary ---------------------------------------------------------------
echo "------------------------------------------------------------------------"
for line in "${summary[@]}"; do
  echo "$line"
done
echo "------------------------------------------------------------------------"
total=${#AGENTS[@]}
attempted=$((pass + fail))
echo "smoke: $pass/$attempted agents passed ($skip skipped, $fail failed) · cards $cards_ok/$total ok"

if (( pass < 1 )); then
  echo "smoke: no agent passed both phases" >&2
  exit 1
fi
if (( STRICT )) && (( fail > 0 )); then
  echo "smoke: --strict and $fail agent(s) failed" >&2
  exit 1
fi
exit 0
