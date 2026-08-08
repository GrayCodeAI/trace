#!/usr/bin/env bash
set -euo pipefail

KEEP_TEMP=false

usage() {
    cat <<'EOF'
Usage: scripts/test-copilot-token-metadata.sh [--keep-temp]

Creates a disposable repo, drives a mock two-turn Copilot session through the real
Entire hooks, and prints the resulting raw checkpoint metadata plus session state.

Options:
  --keep-temp   Keep the temp repo and mock Copilot session files for inspection
  --help        Show this help text
EOF
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --keep-temp)
            KEEP_TEMP=true
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            printf 'Unknown option: %s\n' "$1" >&2
            usage >&2
            exit 1
            ;;
    esac
done

if ! command -v jq >/dev/null 2>&1; then
    printf 'jq is required\n' >&2
    exit 1
fi

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/entire-copilot-token-metadata.XXXXXX")"
REPO_DIR="$TMP_DIR/repo"
BIN_DIR="$TMP_DIR/bin"
SESSION_ROOT="$TMP_DIR/copilot-sessions"
SESSION_ID="mock-copilot-token-session"
TRANSCRIPT_DIR="$SESSION_ROOT/$SESSION_ID"
TRANSCRIPT="$TRANSCRIPT_DIR/events.jsonl"

cleanup() {
    if [[ "$KEEP_TEMP" == "true" ]]; then
        printf 'Kept temp directory: %s\n' "$TMP_DIR"
        return
    fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

mkdir -p "$REPO_DIR" "$BIN_DIR" "$TRANSCRIPT_DIR"

(cd "$ROOT_DIR" && GOCACHE="$TMP_DIR/go-build" go build -o "$BIN_DIR/entire" ./cmd/entire)

export PATH="$BIN_DIR:$PATH"
export COPILOT_PROJECT_DIR="$ROOT_DIR"
export ENTIRE_TEST_COPILOT_SESSION_DIR="$SESSION_ROOT"
export ENTIRE_TEST_TTY=1

run_hook() {
    local hook_name="$1"
    local payload="$2"

    printf '%s' "$payload" | entire hooks copilot-cli "$hook_name" >/dev/null
}

write_turn1_transcript() {
    cat > "$TRANSCRIPT" <<EOF
{"type":"session.start","data":{"sessionId":"$SESSION_ID"},"id":"1","timestamp":"2026-03-17T00:00:00Z","parentId":""}
{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"id":"2","timestamp":"2026-03-17T00:00:01Z","parentId":"1"}
{"type":"user.message","data":{"content":"Create alpha.txt with alpha"},"id":"3","timestamp":"2026-03-17T00:00:02Z","parentId":""}
{"type":"assistant.message","data":{"content":"Created alpha.txt","outputTokens":10},"id":"4","timestamp":"2026-03-17T00:00:03Z","parentId":"3"}
{"type":"tool.execution_complete","data":{"toolCallId":"tool-1","model":"claude-sonnet-4.6","toolTelemetry":{"properties":{"filePaths":"[\\"alpha.txt\\"]"},"metrics":{"linesAdded":1,"linesRemoved":0}}},"id":"5","timestamp":"2026-03-17T00:00:04Z","parentId":"4"}
EOF
}

write_turn2_transcript() {
    cat > "$TRANSCRIPT" <<EOF
{"type":"session.start","data":{"sessionId":"$SESSION_ID"},"id":"1","timestamp":"2026-03-17T00:00:00Z","parentId":""}
{"type":"session.model_change","data":{"newModel":"claude-sonnet-4.6"},"id":"2","timestamp":"2026-03-17T00:00:01Z","parentId":"1"}
{"type":"user.message","data":{"content":"Create alpha.txt with alpha"},"id":"3","timestamp":"2026-03-17T00:00:02Z","parentId":""}
{"type":"assistant.message","data":{"content":"Created alpha.txt","outputTokens":10},"id":"4","timestamp":"2026-03-17T00:00:03Z","parentId":"3"}
{"type":"tool.execution_complete","data":{"toolCallId":"tool-1","model":"claude-sonnet-4.6","toolTelemetry":{"properties":{"filePaths":"[\\"alpha.txt\\"]"},"metrics":{"linesAdded":1,"linesRemoved":0}}},"id":"5","timestamp":"2026-03-17T00:00:04Z","parentId":"4"}
{"type":"user.message","data":{"content":"Create beta.txt with beta"},"id":"6","timestamp":"2026-03-17T00:00:05Z","parentId":""}
{"type":"assistant.message","data":{"content":"Created beta.txt","outputTokens":25},"id":"7","timestamp":"2026-03-17T00:00:06Z","parentId":"6"}
{"type":"tool.execution_complete","data":{"toolCallId":"tool-2","model":"claude-sonnet-4.6","toolTelemetry":{"properties":{"filePaths":"[\\"beta.txt\\"]"},"metrics":{"linesAdded":1,"linesRemoved":0}}},"id":"8","timestamp":"2026-03-17T00:00:07Z","parentId":"7"}
{"type":"session.shutdown","data":{"modelMetrics":{"claude-sonnet-4.6":{"requests":{"count":2},"usage":{"inputTokens":500,"outputTokens":35,"cacheReadTokens":20,"cacheWriteTokens":10}}}},"id":"9","timestamp":"2026-03-17T00:00:08Z","parentId":""}
EOF
}

checkpoint_metadata_json() {
    local checkpoint_id="$1"
    git show "entire/checkpoints/v1:${checkpoint_id:0:2}/${checkpoint_id:2}/0/metadata.json"
}

printf 'Working directory: %s\n' "$TMP_DIR"

cd "$REPO_DIR"
git init >/dev/null
git config user.name "Entire Test"
git config user.email "entire@example.com"
git config commit.gpgsign false
printf '.entire/\n' > .gitignore
printf 'start\n' > README.md
git add .gitignore README.md
git commit -m "init" >/dev/null
git checkout -b feature/copilot-token-metadata >/dev/null

entire enable --agent copilot-cli >/dev/null

run_hook session-start "{\"timestamp\":1771480081383,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"source\":\"new\",\"initialPrompt\":\"Create alpha.txt with alpha\"}"

write_turn1_transcript
run_hook user-prompt-submitted "{\"timestamp\":1771480081360,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"prompt\":\"Create alpha.txt with alpha\"}"
printf 'alpha\n' > alpha.txt
run_hook agent-stop "{\"timestamp\":1771480085412,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"transcriptPath\":\"$TRANSCRIPT\",\"stopReason\":\"end_turn\"}"
git add alpha.txt
git commit -m "Add alpha" >/dev/null
CP1="$(git log -1 --format=%B | sed -n 's/^Entire-Checkpoint: //p' | tail -n1)"

write_turn2_transcript
run_hook user-prompt-submitted "{\"timestamp\":1771481081360,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"prompt\":\"Create beta.txt with beta\"}"
printf 'beta\n' > beta.txt
run_hook agent-stop "{\"timestamp\":1771481085412,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"transcriptPath\":\"$TRANSCRIPT\",\"stopReason\":\"end_turn\"}"
git add beta.txt
git commit -m "Add beta" >/dev/null
CP2="$(git log -1 --format=%B | sed -n 's/^Entire-Checkpoint: //p' | tail -n1)"
run_hook session-end "{\"timestamp\":1771481085425,\"cwd\":\"$REPO_DIR\",\"sessionId\":\"$SESSION_ID\",\"reason\":\"complete\"}"

STATE_FILE="$REPO_DIR/.git/entire-sessions/$SESSION_ID.json"
CP1_METADATA="$(checkpoint_metadata_json "$CP1")"
CP2_METADATA="$(checkpoint_metadata_json "$CP2")"

printf '\nCheckpoint 1: %s\n' "$CP1"
printf '%s\n' "$CP1_METADATA" | jq .

printf '\nCheckpoint 2: %s\n' "$CP2"
printf '%s\n' "$CP2_METADATA" | jq .

printf '\nSession state: %s\n' "$STATE_FILE"
jq . "$STATE_FILE"

jq -e '
  .token_usage.input_tokens == 0 and
  .token_usage.output_tokens == 10 and
  .token_usage.cache_read_tokens == 0 and
  .token_usage.cache_creation_tokens == 0 and
  .token_usage.api_call_count == 1
' <<<"$CP1_METADATA" >/dev/null

jq -e '
  .token_usage.input_tokens == 0 and
  .token_usage.output_tokens == 25 and
  .token_usage.cache_read_tokens == 0 and
  .token_usage.cache_creation_tokens == 0 and
  .token_usage.api_call_count == 1
' <<<"$CP2_METADATA" >/dev/null

jq -e '
  .token_usage.input_tokens == 500 and
  .token_usage.output_tokens == 35 and
  .token_usage.cache_read_tokens == 20 and
  .token_usage.cache_creation_tokens == 10 and
  .token_usage.api_call_count == 2
' "$STATE_FILE" >/dev/null

printf '\nAssertions passed.\n'
printf 'Checkpoint metadata stayed scoped while session state backfilled the full Copilot aggregate.\n'
