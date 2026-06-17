http-cmd-runner — run Linux/macOS/Windows commands over a simple HTTP API
=========================================================================

No install, no build, no dependencies. One static binary. Unzip and run.

QUICK START
-----------
1. Unzip this archive.
2. Run the binary (it reads config.json from the same folder):

     Linux / macOS:
       chmod +x ./http-cmd-runner-*       # one-time
       ./http-cmd-runner-*  -config config.json

     Windows (PowerShell):
       .\http-cmd-runner-windows-amd64.exe -config config.json

   It starts on the address in config.json (default 0.0.0.0:8080).

3. Call the API from anything that can reach it:

     curl http://SERVER_IP:8080/exec \
       -H "Content-Type: application/json" \
       -d '{"command":"uptime"}'

   Health check:  curl http://SERVER_IP:8080/healthz   -> "ok"

THE API
-------
POST /exec   with a JSON body:

   { "command": "ls -la / && df -h" }      # any shell command line
   { "command": "cat", "stdin": "hi\n" }   # feed stdin
   { "command": "sleep 2", "timeout_sec": 5 }

Response (the command's output, parsed back as JSON):

   { "stdout": "...", "stderr": "...", "exit_code": 0,
     "duration_ms": 12, "timed_out": false, "truncated": false }

CONFIG (config.json)
--------------------
   listen           "0.0.0.0:8080"  -> reachable from other machines.
                    Use "127.0.0.1:8080" for localhost-only.
   allow_arbitrary  true  -> any command runs (default).
                    false -> only commands listed in "allowlist" run.
   default_timeout_sec / max_timeout_sec / max_output_bytes / working_dir

NOTE: there is no authentication and (by default) every command is allowed.
Anyone who can reach the port can run commands on this host. Only run it on
networks you trust, or restrict the port with your firewall.

More docs: https://github.com/roshandubey-cloud/utilities/tree/main/http-cmd-runner
