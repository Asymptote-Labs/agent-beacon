package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/image"
	"github.com/asymptote-labs/agent-beacon/beacon-sandbox/sandbox"
)

// Guest paths for the forwarding probe. The secrets file is where a user-mode `beacon endpoint
// connect` puts it, so Vector reads the key the way the managed forwarder does.
const (
	forwardBaseDir     = image.AgentHome + "/.beacon/endpoint/asymptote"
	forwardSecretsFile = forwardBaseDir + "/vector-secrets.json"
	forwardDataDir     = forwardBaseDir + "/vector-data"
	forwardPackDir     = forwardBaseDir + "/pack"
	forwardVectorOut   = forwardBaseDir + "/vector.out"
	forwardIngestDir   = "/tmp/beacon-ingest"
	forwardStandInPath = "/tmp/beacon-ingest-standin.py"
)

// Artifact names in the run directory, read back by the offline judge.
const (
	ForwardedLogName     = "forwarded-runtime.ndjson"
	ForwardRequestsName  = "forward-requests.jsonl"
	ForwardVectorOutName = "vector.out"
	// ForwardRuntimeName is a second copy of the runtime log taken after forwarding finished.
	// runtime.jsonl is collected earlier, at quiescence, and Vector may ship a line written after
	// that; comparing against the earlier copy would report such a line as invented.
	ForwardRuntimeName = "runtime-after-forwarding.jsonl"
)

// forwardStandIn serves the three ingest routes Vector's Asymptote pack calls, on loopback.
//
// It checks the bearer token against the secrets file and records only whether it matched, so the
// key never reaches its output. Runtime and inventory bodies are gunzipped and appended one line
// per event, which is the NDJSON contract of the real service.
const forwardStandIn = `import gzip, http.server, json, os, sys, threading

out_dir, secrets_path = sys.argv[1], sys.argv[2]
lock = threading.Lock()
routes = {"/v1/ingest/runtime": "runtime.ndjson", "/v1/ingest/inventory": "inventory.ndjson"}

def device_key():
    try:
        with open(secrets_path) as f:
            return json.load(f).get("device_key", "")
    except Exception:
        return ""

def append(name, text):
    with lock, open(os.path.join(out_dir, name), "a") as f:
        f.write(text)

class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def auth_ok(self):
        key = device_key()
        return bool(key) and self.headers.get("Authorization", "") == "Bearer " + key

    def body(self):
        if self.headers.get("Transfer-Encoding", "").lower() == "chunked":
            data = b""
            while True:
                size = int(self.rfile.readline().strip() or b"0", 16)
                if size == 0:
                    self.rfile.readline()
                    return data
                data += self.rfile.read(size)
                self.rfile.readline()
        return self.rfile.read(int(self.headers.get("Content-Length") or 0))

    def reply(self, code):
        self.send_response(code)
        self.send_header("Content-Length", "0")
        self.end_headers()

    def do_GET(self):
        ok = self.auth_ok()
        append("requests.jsonl", json.dumps({"method": self.command, "path": self.path, "auth_ok": ok}) + "\n")
        self.reply(200 if ok else 401)

    # Vector's http sink healthcheck sends HEAD, not GET.
    do_HEAD = do_GET

    def do_POST(self):
        data = self.body()
        encoding = self.headers.get("Content-Encoding", "")
        if encoding == "gzip":
            data = gzip.decompress(data)
        lines = [l for l in data.decode("utf-8", "replace").split("\n") if l.strip()]
        ok = self.auth_ok()
        name = routes.get(self.path)
        if ok and name:
            append(name, "".join(l + "\n" for l in lines))
        append("requests.jsonl", json.dumps({"method": "POST", "path": self.path, "auth_ok": ok,
                                             "lines": len(lines), "encoding": encoding}) + "\n")
        self.reply(401 if not ok else (200 if name else 404))

    def log_message(self, *args):
        pass

server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(os.path.join(out_dir, "port.tmp"), "w") as f:
    f.write(str(server.server_address[1]))
os.rename(os.path.join(out_dir, "port.tmp"), os.path.join(out_dir, "port"))
server.serve_forever()
`

// forwardSetup starts the stand-in and the bundled Vector, then writes the validation event.
//
// Runs after install and before the session, because the pack reads the runtime log from the end:
// anything written before Vector starts is never shipped, which is the managed forwarder's
// contract too.
//
// The device key is generated inside the guest with shell builtins and written straight to the
// 0600 secrets file. Every script passed to Exec lands in a process's argv, so a key made on the
// host would be disclosed by the harness itself. Nothing outside the guest ever holds it; the leak
// checks search for its shape instead.
func forwardSetup(ctx context.Context, g guest, agent sandbox.ExecOpts, logPath string,
	art *Artifacts, logf func(string, ...any)) error {

	art.Meta["forward_ran"] = "true"
	fail := func(format string, a ...any) error {
		err := fmt.Errorf(format, a...)
		art.Meta["forward_error"] = oneLine(err.Error())
		logf("forwarding: %v", err)
		return err
	}

	start := fmt.Sprintf(`set -eu
umask 077
mkdir -p %[1]s %[2]s %[3]s
chmod 0700 %[1]s
key="bcn_device_test_$(od -An -tx1 -N16 /dev/urandom | tr -d ' \n')"
printf '{"device_key": "%%s"}\n' "$key" > %[4]s
unset key
chmod 0600 %[4]s
cat > %[5]s <<'PY'
%[6]sPY
rm -f %[3]s/port
setsid nohup python3 %[5]s %[3]s %[4]s > %[3]s/standin.log 2>&1 < /dev/null &
i=0
while [ ! -s %[3]s/port ] && [ "$i" -lt 50 ]; do i=$((i+1)); sleep 0.2; done
if [ ! -s %[3]s/port ]; then echo "the ingest stand-in did not start: $(tail -c 400 %[3]s/standin.log)"; exit 1; fi
echo "FORWARD_PORT=$(cat %[3]s/port)"`,
		forwardBaseDir, forwardDataDir, forwardIngestDir, forwardSecretsFile, forwardStandInPath, forwardStandIn)
	r, err := g.Exec(ctx, start, agent)
	if err != nil || r.ExitCode != 0 {
		return fail("could not start the ingest stand-in (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stdout+r.Stderr))
	}
	port := ""
	for _, line := range strings.Split(r.Stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "FORWARD_PORT="); ok {
			port = v
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return fail("the ingest stand-in reported no port: %s", oneLine(r.Stdout))
	}
	art.Meta["forward_port"] = port

	env := fmt.Sprintf("BEACON_ASYMPTOTE_INGEST_URL=http://127.0.0.1:%s BEACON_ASYMPTOTE_SECRETS_FILE=%s "+
		"BEACON_ASYMPTOTE_DATA_DIR=%s", port, forwardSecretsFile, forwardDataDir)
	pack := fmt.Sprintf(`set -eu
beacon endpoint asymptote install-pack --user --output %[1]s --log-path %[2]s
export %[3]s
%[4]s --version
%[4]s validate --skip-healthchecks %[1]s/vector.toml`,
		forwardPackDir, shq(logPath), env, image.VectorPath)
	r, err = g.Exec(ctx, pack+" 2>&1", agent)
	art.Meta["forward_pack_validate"] = oneLine(r.Stdout)
	if err != nil || r.ExitCode != 0 {
		return fail("vector rejected the Asymptote pack (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stdout))
	}
	for _, line := range strings.Split(r.Stdout, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "vector ") {
			art.Meta["forward_vector_version"] = oneLine(line)
			break
		}
	}

	// Started by the packaged path, then identified from /proc: that is what the check compares
	// against, rather than trusting the path this script typed.
	//
	// Ready means Vector's startup healthcheck reached the stand-in, which happens after its
	// sources are built. The extra seconds cover the file source's first scan, which records the
	// end of the log; a line written before that is not shipped.
	run := fmt.Sprintf(`set -u
export %[1]s
setsid nohup %[2]s --config %[3]s/vector.toml > %[4]s 2>&1 < /dev/null &
i=0
while [ "$i" -lt 60 ]; do
  pid="$(pgrep -n -x vector || true)"
  if [ -n "$pid" ] && grep -q '"path": "/v1/ingest/health"' %[5]s/requests.jsonl 2>/dev/null; then break; fi
  i=$((i+1)); sleep 0.5
done
pid="$(pgrep -n -x vector || true)"
if [ -z "$pid" ]; then echo "vector exited: $(tail -c 600 %[4]s)"; exit 1; fi
if ! grep -q '"path": "/v1/ingest/health"' %[5]s/requests.jsonl 2>/dev/null; then
  echo "vector never called the health check: $(tail -c 600 %[4]s)"; exit 1
fi
sleep 3
echo "FORWARD_VECTOR_EXE=$(readlink /proc/$pid/exe)"`,
		env, image.VectorPath, forwardPackDir, forwardVectorOut, forwardIngestDir)
	r, err = g.Exec(ctx, run, agent)
	for _, line := range strings.Split(r.Stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "FORWARD_VECTOR_EXE="); ok {
			art.Meta["forward_vector_exe"] = v
		}
	}
	if err != nil || r.ExitCode != 0 {
		return fail("vector did not start (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stdout))
	}
	logf("forwarding: vector %s running from %s, stand-in on 127.0.0.1:%s",
		art.Meta["forward_vector_version"], art.Meta["forward_vector_exe"], port)

	r, err = g.Exec(ctx, fmt.Sprintf("beacon endpoint asymptote validate --user --log-path %s 2>&1", shq(logPath)), agent)
	art.Meta["forward_validate_output"] = oneLine(r.Stdout)
	if err != nil || r.ExitCode != 0 {
		return fail("beacon endpoint asymptote validate failed (rc=%d): %v %s", r.ExitCode, err, oneLine(r.Stdout))
	}
	return nil
}

// forwardCollect waits for the session's events to reach the stand-in, then brings its record
// home.
//
// The pack batches for up to 60 seconds, so the wait is a poll for the two things the check needs,
// bounded well past one batch. It is skipped when setup failed, since nothing is running to wait
// for; whatever the stand-in and Vector wrote is still collected. Vector and the stand-in are
// stopped afterwards; on a disposable sandbox that is tidiness, on a machine that outlives the run
// it is required.
func forwardCollect(ctx context.Context, g guest, agent sandbox.ExecOpts, logPath, canary, runDir string,
	setupOK bool, art *Artifacts, logf func(string, ...any)) {

	if !setupOK {
		collectForwardArtifacts(ctx, g, agent, logPath, runDir, art, logf)
		return
	}
	start := time.Now()
	wait := fmt.Sprintf(`i=0
f=%[1]s/runtime.ndjson
while [ "$i" -lt 75 ]; do
  if grep -q 'asymptote_managed_http' "$f" 2>/dev/null && grep -q -F %[2]s "$f" 2>/dev/null; then echo FORWARD_ARRIVED; break; fi
  i=$((i+1)); sleep 2
done
if [ -n "$(pgrep -n -x vector || true)" ]; then echo VECTOR_ALIVE=true; else echo VECTOR_ALIVE=false; fi`,
		forwardIngestDir, shq(canary))
	r, err := g.Exec(ctx, wait, agent)
	art.Meta["forward_wait_seconds"] = fmt.Sprintf("%.0f", time.Since(start).Seconds())
	art.Meta["forward_arrived"] = fmt.Sprintf("%v", err == nil && strings.Contains(r.Stdout, "FORWARD_ARRIVED"))
	art.Meta["forward_vector_alive"] = fmt.Sprintf("%v", strings.Contains(r.Stdout, "VECTOR_ALIVE=true"))
	logf("forwarding: arrived=%s after %ss, vector alive=%s",
		art.Meta["forward_arrived"], art.Meta["forward_wait_seconds"], art.Meta["forward_vector_alive"])

	collectForwardArtifacts(ctx, g, agent, logPath, runDir, art, logf)
}

// collectForwardArtifacts stops Vector and the stand-in and copies their records home.
func collectForwardArtifacts(ctx context.Context, g guest, agent sandbox.ExecOpts, logPath, runDir string,
	art *Artifacts, logf func(string, ...any)) {

	_, _ = g.Exec(ctx, "pkill -x vector; pkill -f "+shq(forwardStandInPath)+"; true", agent)

	for remote, name := range map[string]string{
		forwardIngestDir + "/runtime.ndjson": ForwardedLogName,
		forwardIngestDir + "/requests.jsonl": ForwardRequestsName,
		forwardVectorOut:                     ForwardVectorOutName,
		logPath:                              ForwardRuntimeName,
	} {
		if err := g.Get(ctx, remote, filepath.Join(runDir, name)); err != nil {
			art.Meta["forward_missing_"+strings.TrimSuffix(name, filepath.Ext(name))] = oneLine(err.Error())
			logf("forwarding: could not collect %s: %v", remote, err)
		}
	}
	art.ForwardedLog = filepath.Join(runDir, ForwardedLogName)
}
