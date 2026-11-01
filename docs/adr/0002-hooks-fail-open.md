# Hooks fail open

A hook stops waiting for the daemon after about two seconds, and the session continues terminal-only. After a restart the daemon reports the gap. The bridge must never be what stops a session.
