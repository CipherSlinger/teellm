#!/usr/bin/env sh
set -eu
DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
export OLLAMA_HOST="${OLLAMA_HOST:-127.0.0.1:11434}"
export OLLAMA_MODELS="${OLLAMA_MODELS:-$DIR/models/models}"
export OLLAMA_LIBRARY_PATH="${OLLAMA_LIBRARY_PATH:-$DIR/lib/ollama}"
exec "$DIR/ollama" serve
