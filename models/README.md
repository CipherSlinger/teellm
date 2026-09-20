# TEE-LLM Models and Runtime Directory

This directory houses the offline Ollama inference runtime and model manifests/weights used by the TEE-LLM service.

## Directory Structure

- `ollama/`: Root of the Ollama runtime and offline weight bundle.
  - `ollama`: Ollama server executable.
  - `start-ollama.sh`: Wrapper script launching `ollama serve` with configured models and library paths.
  - `lib/ollama/`: Dynamic shared libraries (`libllama-server-impl.so`, CUDA/ROCm/CPU backends).
  - `models/models/`: Standard Ollama model store containing:
    - `manifests/`: Model layer metadata and digests.
    - `blobs/`: Actual tensor weights and configurations (sha256-*).

## Managing Models via CLI

Use `./deploy.sh models` from the `teellm` root:
- `./deploy.sh models list`: List all available models and their readiness status.
- `./deploy.sh models info <model>`: Display details and verify weight integrity.
- `./deploy.sh models switch <model>`: Switch active default model in configuration files.

## Adding New Models

1. Pull or copy the model into Ollama format:
   ```bash
   OLLAMA_MODELS="$(pwd)/models/ollama/models/models" ./models/ollama/ollama pull <model_name>
   ```
2. Add recommended parameters in `configs/models.json`.
3. Verify readiness using `./deploy.sh models info <model_name>`.
