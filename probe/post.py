"""Step-1 probe (throwaway): post one message to a session's messaging socket.

    python3 -I post.py <socket> <text>

With PROBE_AUTH=1, sends {"type":"auth","token":...} first, taking the token
from CLAUDE_CODE_MESSAGING_TOKEN. Prints whatever the socket sends back within
3 s. Never prints the token.
"""

import json
import os
import socket
import sys

path, text = sys.argv[1], sys.argv[2]
lines = []
if os.environ.get("PROBE_AUTH") == "1":
    lines.append({"type": "auth", "token": os.environ["CLAUDE_CODE_MESSAGING_TOKEN"]})
lines.append({"type": "user", "message": {"role": "user", "content": text}})

s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
s.settimeout(3)
s.connect(path)
s.sendall(b"".join(json.dumps(l).encode() + b"\n" for l in lines))
s.shutdown(socket.SHUT_WR)
print("sent", len(lines), "lines; auth" if len(lines) == 2 else "lines; no auth")
reply = b""
try:
    while chunk := s.recv(4096):
        reply += chunk
except socket.timeout:
    print("reply timed out after 3 s")
print("reply:", repr(reply.decode("utf-8", "replace")))
