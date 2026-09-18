#!/usr/bin/env bash
# TEE-LLM autostart wrapper: ensures ollama is active, starts teellm-service, retries on exit.
# Manual mode: touch /root/taa/manual or /root/taa/manual-teellm to pause; rm to resume.
set -u
cd /root/taa || exit 1
LOG=/root/taa/teellm-service.log
OLLAMA_DIR="/root/taa/ollama-qwen"
OLLAMA_HOST="127.0.0.1:11434"
TEEPID=""

cleanup() {
  if [ -n "$TEEPID" ]; then
    kill -TERM "$TEEPID" 2>/dev/null || true
  fi
  exit 0
}
trap cleanup SIGTERM SIGINT

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): teellm autostart wrapper started" >> "$LOG"

# Step 1: Ensure Ollama backend is running
ensure_ollama() {
  if curl -fsS "http://${OLLAMA_HOST}/api/tags" >/dev/null 2>&1; then
    return 0
  fi
  if [ -x "${OLLAMA_DIR}/start-ollama.sh" ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): starting ollama daemon via ${OLLAMA_DIR}/start-ollama.sh" >> "$LOG"
    (cd "$OLLAMA_DIR" && exec ./start-ollama.sh >> /root/taa/ollama.log 2>&1) &
    for _ in {1..30}; do
      if curl -fsS "http://${OLLAMA_HOST}/api/tags" >/dev/null 2>&1; then
        echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): ollama daemon ready" >> "$LOG"
        return 0
      fi
      sleep 1
    done
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): warning: ollama did not respond in 30s" >> "$LOG"
  fi
}

ensure_ollama

# Step 2: Supervise teellm-service
while true; do
  if [ -f ./manual ] || [ -f ./manual-teellm ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): manual mode active, teellm autostart paused" >> "$LOG"
    sleep 10
    continue
  fi
  if [ ! -x ./teellm-service ]; then
    echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): ./teellm-service not found, retry in 30s" >> "$LOG"
    sleep 30
    continue
  fi

  CONFIG_ARG=""
  if [ -f ./configs/teellm-docker.json ]; then
    CONFIG_ARG="-config ./configs/teellm-docker.json"
  elif [ -f ./teellm-docker.json ]; then
    CONFIG_ARG="-config ./teellm-docker.json"
  fi

  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): starting ./teellm-service ${CONFIG_ARG}" >> "$LOG"
  ./teellm-service ${CONFIG_ARG} >> "$LOG" 2>&1 &
  TEEPID=$!
  wait "$TEEPID"
  EXIT_CODE=$?
  TEEPID=""
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ): teellm-service exited with code ${EXIT_CODE}, restart in 10s" >> "$LOG"
  sleep 10
done
