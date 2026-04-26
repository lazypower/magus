# Ollama runs as a system container on port 11434.
# Point the CLI client at it so `ollama run`, `ollama pull`, etc. work from the shell.
export OLLAMA_HOST=http://localhost:11434
