"""Step-3 probe (throwaway): a minimal stdio MCP server that logs its context.

    python3 -I mcp_env.py <log-file>

Logs the names of CLAUDE*/TMUX* environment variables (values only for the
session ID, PID and project dir; the messaging token by length), the parent
PID and the parent's executable. Answers initialize, tools/list and one tool,
probe_echo, so Claude Code keeps the server.
"""

import json
import os
import sys
import time

log = open(sys.argv[1], "a")
env = {k: v for k, v in os.environ.items() if k.startswith(("CLAUDE", "TMUX"))}
shown = {"CLAUDE_CODE_SESSION_ID", "CLAUDE_PID", "CLAUDE_PROJECT_DIR", "TMUX_PANE"}
ppid = os.getppid()
try:
    parent_exe = os.readlink(f"/proc/{ppid}/exe")
except OSError as e:
    parent_exe = repr(e)
log.write(json.dumps({
    "t": time.time(),
    "ppid": ppid,
    "parent_exe": parent_exe,
    "env_names": sorted(env),
    "env_values": {k: env[k] for k in sorted(shown & env.keys())},
    "token_len": len(env.get("CLAUDE_CODE_MESSAGING_TOKEN", "")),
}) + "\n")
log.flush()

for line in sys.stdin:
    msg = json.loads(line)
    log.write(json.dumps({"t": time.time(), "in": msg}) + "\n")
    log.flush()
    if "id" not in msg:
        continue
    method = msg.get("method")
    if method == "initialize":
        result = {
            "protocolVersion": msg["params"]["protocolVersion"],
            "capabilities": {"tools": {}},
            "serverInfo": {"name": "probe", "version": "0"},
            "instructions": "Probe server. Call probe_echo only when asked.",
        }
    elif method == "tools/list":
        result = {"tools": [{
            "name": "probe_echo",
            "description": "Echo the text back.",
            "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]},
        }]}
    elif method == "tools/call":
        text = msg["params"].get("arguments", {}).get("text", "")
        result = {"content": [{"type": "text", "text": "echo: " + text}]}
    else:
        result = {}
    sys.stdout.write(json.dumps({"jsonrpc": "2.0", "id": msg["id"], "result": result}) + "\n")
    sys.stdout.flush()
