"""
Webhook server that triggers the Claude loop in a Docker container.

POST /webhook  →  docker run claude-loop (if not already running)
GET  /status   →  container running or idle
"""

import subprocess
from pathlib import Path

from fastapi import FastAPI, HTTPException
from fastapi.responses import JSONResponse

app = FastAPI()

IMAGE = "claude-loop"
CONTAINER_NAME = "claude-loop-run"
MONITOR_PORT = 7682
CLAUDE_DIR = str(Path.home() / ".claude")
CLAUDE_JSON = str(Path.home() / ".claude.json")


def _is_running() -> bool:
    result = subprocess.run(
        ["docker", "inspect", "--format", "{{.State.Running}}", CONTAINER_NAME],
        capture_output=True, text=True,
    )
    return result.stdout.strip() == "true"


@app.post("/webhook")
def trigger():
    if _is_running():
        raise HTTPException(status_code=409, detail="loop already running")

    subprocess.Popen([
        "docker", "run",
        "--rm",
        "--name", CONTAINER_NAME,
        "-p", f"{MONITOR_PORT}:{MONITOR_PORT}",
        "-v", f"{CLAUDE_DIR}:/home/ubuntu/.claude",
        "-v", f"{CLAUDE_JSON}:/home/ubuntu/.claude.json",
        IMAGE,
    ])

    return JSONResponse({"status": "started", "monitor": f"http://localhost:{MONITOR_PORT}"})


@app.get("/status")
def status():
    running = _is_running()
    return {"status": "running" if running else "idle"}
