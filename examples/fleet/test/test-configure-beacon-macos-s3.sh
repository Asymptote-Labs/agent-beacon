#!/bin/sh
# Tests for examples/fleet/configure-beacon-macos-s3.sh. Each case starts
# fake_fleet.py on a free local port, runs the real helper against it with real
# curl, and checks the requests the fake recorded. No Fleet server, GitHub, or
# AWS is involved; the package is a small stand-in file.
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)"
HELPER="$ROOT_DIR/examples/fleet/configure-beacon-macos-s3.sh"
FAKE="$ROOT_DIR/examples/fleet/test/fake_fleet.py"
SQL_DIR="$ROOT_DIR/packaging/macos/fleet"
TMP_DIR="$(mktemp -d)"
FAKE_PID=""

cleanup() {
  if [ -n "$FAKE_PID" ]; then
    kill "$FAKE_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT INT TERM

command -v python3 >/dev/null 2>&1 || { echo "python3 is required for the Fleet helper tests" >&2; exit 1; }
command -v bash >/dev/null 2>&1 || { echo "bash is required for the Fleet helper tests" >&2; exit 1; }

fail() {
  echo "FAIL: $*" >&2
  if [ -n "${CASE_DIR:-}" ] && [ -f "$CASE_DIR/err" ]; then
    echo "--- helper stderr ---" >&2
    tail -n 20 "$CASE_DIR/err" >&2
  fi
  exit 1
}

PKG="$TMP_DIR/BeaconEndpointAgent-9.9.9-arm64.pkg"
printf 'stand-in beacon package 9.9.9\n' >"$PKG"
PKG_NEW="$TMP_DIR/BeaconEndpointAgent-9.9.10-arm64.pkg"
printf 'stand-in beacon package 9.9.10\n' >"$PKG_NEW"
KEYS="AWS_ACCESS_KEY_ID=AKIAEXAMPLEKEY0000000 AWS_SECRET_ACCESS_KEY=example-secret-value"

# start_fake NAME FIXTURE_JSON
start_fake() {
  CASE_DIR="$TMP_DIR/$1"
  mkdir -p "$CASE_DIR"
  printf '%s\n' "$2" >"$CASE_DIR/fixture.json"
  LOG="$CASE_DIR/requests.jsonl"
  MARK=0
  python3 "$FAKE" --fixture "$CASE_DIR/fixture.json" --log "$LOG" --port-file "$CASE_DIR/port" --pkg "$PKG" &
  FAKE_PID=$!
  tries=0
  while [ ! -s "$CASE_DIR/port" ]; do
    tries=$((tries + 1))
    [ "$tries" -lt 50 ] || fail "fake Fleet did not start"
    sleep 0.1
  done
  PORT="$(cat "$CASE_DIR/port")"
}

stop_fake() {
  kill "$FAKE_PID" 2>/dev/null || true
  wait "$FAKE_PID" 2>/dev/null || true
  FAKE_PID=""
}

# run_helper [NAME=VALUE...] -- [helper args...]: run the helper non-interactively
# against the current fake, with stdout and stderr captured. Sets STATUS.
run_helper() {
  set -- "$@"
  envs=""
  while [ "$#" -gt 0 ] && [ "$1" != "--" ]; do
    envs="$envs $1"
    shift
  done
  [ "$#" -gt 0 ] && shift
  set +e
  # shellcheck disable=SC2086
  env -i PATH="$PATH" HOME="$CASE_DIR" TMPDIR="$CASE_DIR" \
    FLEET_URL="http://127.0.0.1:$PORT" FLEET_TOKEN=test-token FLEET_TEAM_ID=7 \
    BEACON_S3_BUCKET=example-security-logs AWS_REGION=us-west-2 BEACON_S3_PREFIX=beacon-prod \
    BEACON_UPDATE_MANIFEST_URL="http://127.0.0.1:$PORT/manifest.json" \
    $envs bash "$HELPER" --yes "$@" </dev/null >"$CASE_DIR/out" 2>"$CASE_DIR/err"
  STATUS=$?
  set -e
}

mark() {
  MARK="$(wc -l <"$LOG" | tr -d ' ')"
}

# logq EXPR: evaluate a Python expression over the requests recorded since the
# last mark. R is the list of requests; see the helpers below.
logq() {
  python3 - "$LOG" "${MARK:-0}" "$SQL_DIR" "$1" <<'PY'
import json, os, sys
log, mark, sql_dir, expr = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]
R = [json.loads(line) for line in open(log).read().splitlines()[mark:]]
def sql(name):
    return open(os.path.join(sql_dir, name + ".sql")).read().strip()
def req(method, path):
    return [r for r in R if r["method"] == method and r["path"] == path]
def writes():
    return [r for r in R if r["method"] != "GET"]
def form(r):
    return r.get("form") or {}
def body(r):
    return r.get("json") or {}
def keys(r):
    return set(form(r)) | set(body(r) if isinstance(body(r), dict) else {})
sys.exit(0 if eval(expr) else 1)
PY
}

expect() {
  logq "$1" || fail "$2"
}

expect_status() {
  case "$1" in
    ok) [ "$STATUS" -eq 0 ] || fail "$2 (exit $STATUS)" ;;
    fail) [ "$STATUS" -ne 0 ] || fail "$2 (exit 0)" ;;
    *) [ "$STATUS" -eq "$1" ] || fail "$2 (exit $STATUS, want $1)" ;;
  esac
}

expect_err() {
  grep -q -- "$1" "$CASE_DIR/err" || fail "$2"
}

POLICY_FILES="
Beacon: installed=policies/beacon-installed
Beacon: collector running=policies/collector-running
Beacon: S3 forwarder running=policies/s3-forwarder-running
Beacon: S3 forwarding configured=policies/s3-forwarding-configured
Beacon: inventory heartbeat within 4 days=policies/inventory-heartbeat-recent"

# The helper embeds the SQL because it is downloaded on its own; it must match
# the shipped files exactly.
awk '/^beacon_sql\(\) \{/{p=1} p{print} p&&/^\}/{exit}' "$HELPER" >"$TMP_DIR/beacon_sql.sh"
for name in policies/beacon-installed policies/collector-running policies/s3-forwarder-running \
  policies/s3-forwarding-configured policies/inventory-heartbeat-recent queries/beacon-version \
  queries/collector-service-health queries/s3-vector-forwarder-health \
  queries/s3-vector-forwarding-configured queries/inventory-heartbeat-age-seconds; do
  bash -c '. "$1"; beacon_sql "$2"' _ "$TMP_DIR/beacon_sql.sh" "$name" | cmp -s - "$SQL_DIR/$name.sql" ||
    fail "SQL embedded in the helper for $name differs from packaging/macos/fleet/$name.sql"
done
for policy in "$SQL_DIR"/policies/*.sql; do
  name="policies/$(basename "$policy" .sql)"
  printf '%s\n' "$POLICY_FILES" | grep -q "=$name\$" || fail "$name is not created by the helper"
done

# --- Fresh team: upload, display name, policies, reports -------------------
start_fake fresh '{}'
run_helper $KEYS --
expect_status ok "fresh run should succeed"
expect 'len(req("GET", "/manifest.json")) == 1 and len(req("GET", "/BeaconEndpointAgent-9.9.9-arm64.pkg")) == 1' \
  "fresh run should download the package named by the manifest"
expect 'len(req("POST", "/api/latest/fleet/software/package")) == 1' "fresh run should upload the package"
expect 'all(form(r)["software"]["filename"] == "BeaconEndpointAgent-9.9.9-arm64.pkg" and form(r)["team_id"] == "7" and form(r)["self_service"] == "false" and "cpu_type LIKE '"'"'arm64%'"'"'" in form(r)["pre_install_query"] and "display_name" not in form(r) for r in req("POST", "/api/latest/fleet/software/package"))' \
  "upload should carry the Apple Silicon pre-install query and leave display_name for the update"
expect 'any(form(r).get("display_name") == "Beacon Endpoint Agent" and "software" not in form(r) for r in R if r["method"] == "PATCH" and r["path"].endswith("/package"))' \
  "fresh run should set the display name with a PATCH that sends no file"
expect 'not any("automatic_install" in keys(r) for r in R)' "no request should send automatic_install"
expect 'not any("fleet_id" in keys(r) or "fleet_id" in r["query"] for r in R)' "requests should name the team as team_id only"
expect 'len(req("POST", "/api/latest/fleet/teams/7/policies")) == 5' "fresh run should create five policies"
while IFS='=' read -r pname pfile; do
  [ -n "$pname" ] || continue
  expect "any(body(r).get(\"name\") == \"$pname\" and body(r).get(\"query\") == sql(\"$pfile\") and body(r).get(\"platform\") == \"darwin\" for r in req(\"POST\", \"/api/latest/fleet/teams/7/policies\"))" \
    "policy '$pname' should be created from $pfile.sql"
done <<EOF
$POLICY_FILES
EOF
expect 'not any("software_title_id" in body(r) for r in req("POST", "/api/latest/fleet/teams/7/policies"))' \
  "without automatic install no policy should install software"
expect 'any(body(r).get("query") == sql("queries/collector-service-health") for r in req("POST", "/api/latest/fleet/queries"))' \
  "reports should use the fixed collector health query"
expect 'len(req("POST", "/api/latest/fleet/queries")) == 5' "fresh run should create five reports"
expect 'sorted(s["name"] for s in body(req("PUT", "/api/latest/fleet/spec/secret_variables")[0])["secrets"]) == ["FLEET_SECRET_BEACON_AWS_ACCESS_KEY_ID", "FLEET_SECRET_BEACON_AWS_SECRET_ACCESS_KEY"]' \
  "fresh run should store the access key pair as secret variables"
expect 'any(form(r)["script"]["filename"] == "beacon-configure-s3-forwarding.sh" and "$FLEET_SECRET_BEACON_AWS_ACCESS_KEY_ID" in form(r)["script"]["content"] and "example-secret-value" not in form(r)["script"]["content"] for r in req("POST", "/api/latest/fleet/scripts"))' \
  "the S3 script should reference the secret variables, never the key itself"

# Rerun against the same server: nothing about the package or policies changed.
mark
run_helper $KEYS --
expect_status ok "rerun should succeed"
expect 'not any(r["path"].startswith("/api/latest/fleet/software/") for r in writes())' \
  "rerun with the same package should not touch the software title"
expect 'not any("/policies" in r["path"] for r in writes())' "rerun should leave unchanged policies alone"

# A new build replaces only the package file.
mark
run_helper $KEYS -- --pkg "$PKG_NEW"
expect_status ok "rerun with a new package should succeed"
expect 'len([r for r in writes() if r["path"].startswith("/api/latest/fleet/software/")]) == 1' \
  "a new build should be a single package update"
expect 'any(sorted(form(r)) == ["software", "team_id"] and form(r)["software"]["filename"] == "BeaconEndpointAgent-9.9.10-arm64.pkg" for r in writes() if r["path"].endswith("/package"))' \
  "a new build should send only the file"
expect 'not req("POST", "/api/latest/fleet/software/package")' "a new build should not add a second package"

# Turning on automatic install attaches the title to the receipt policy.
mark
run_helper $KEYS FLEET_AUTOMATIC_INSTALL=true -- --pkg "$PKG_NEW"
expect_status ok "automatic install run should succeed"
expect 'len([r for r in writes() if "/policies" in r["path"]]) == 1' "only the installed policy should change"
expect 'any(body(r).get("name") == "Beacon: installed" and isinstance(body(r).get("software_title_id"), int) for r in writes() if r["method"] == "PATCH" and "/policies/" in r["path"])' \
  "automatic install should attach software_title_id to 'Beacon: installed'"
stop_fake

# --- Existing title on a second page, plus Fleet's old auto-install policy ---
start_fake legacy '{
  "title_page_size": 1,
  "titles": [
    {"id": 41, "name": "Other", "bundle_identifier": "com.example.other", "software_package": {"name": "Other.pkg"}},
    {"id": 42, "name": "endpoint", "bundle_identifier": "ai.asymptote.beacon.endpoint",
     "software_package": {"name": "BeaconEndpointAgent-1.0.0-arm64.pkg", "hash_sha256": "old", "install_script": "",
                          "uninstall_script": "", "pre_install_query": "", "display_name": "", "self_service": false}}
  ],
  "policies": [
    {"id": 5, "name": "Beacon: installed", "query": "SELECT 1;", "platform": "darwin"},
    {"id": 6, "name": "endpoint (pkg) automatic install", "query": "SELECT 1 FROM apps WHERE bundle_identifier = '"'"'ai.asymptote.beacon.endpoint'"'"';",
     "install_software": {"software_title_id": 42}}
  ]
}'
run_helper $KEYS -- --pkg "$PKG"
expect_status ok "update of an existing title should succeed"
expect 'len([r for r in req("GET", "/api/latest/fleet/software/titles") if r["query"].get("page") == ["1"]]) >= 1' \
  "title lookup should read the second page"
expect 'not req("POST", "/api/latest/fleet/software/package")' "an existing title should not get a second package"
expect 'any(set(form(r)) == {"team_id", "software", "install_script", "uninstall_script", "pre_install_query", "display_name"} for r in req("PATCH", "/api/latest/fleet/software/titles/42/package"))' \
  "an outdated title should get every differing field in one update"
expect 'any(body(r).get("software_title_id") == 42 for r in req("PATCH", "/api/latest/fleet/teams/7/policies/5"))' \
  "the old automatic install should move to 'Beacon: installed'"
expect 'any(body(r).get("ids") == [6] for r in req("POST", "/api/latest/fleet/teams/7/policies/delete"))' \
  "Fleet's never-passing automatic-install policy should be deleted"
stop_fake

# --- Credential handling -----------------------------------------------------
start_fake credentials '{}'
run_helper --
expect_status fail "--yes without AWS keys should fail"
expect_err 'BEACON_AWS_CREDENTIAL_MODE=none' "the error should name the none mode"
expect 'len(R) == 0' "a missing-key run should make no requests"

mark
run_helper BEACON_AWS_CREDENTIAL_MODE=none -- --pkg "$PKG"
expect_status ok "credential mode none should succeed"
expect 'not req("PUT", "/api/latest/fleet/spec/secret_variables")' "mode none should store no secrets"
expect 'any(form(r)["script"]["filename"] == "beacon-configure-s3-forwarding.sh" and "export AWS_ACCESS_KEY_ID" not in form(r)["script"]["content"] for r in req("POST", "/api/latest/fleet/scripts"))' \
  "mode none should upload an S3 script that sets no AWS keys"

mark
run_helper BEACON_AWS_CREDENTIAL_MODE=bogus --
expect_status 2 "an unknown credential mode should exit 2"
expect_err "must be 'keys' or 'none'" "the error should list the valid modes"
expect 'len(R) == 0' "an unknown mode should make no requests"

mark
run_helper $KEYS FLEET_AUTOMATIC_INSTALL=yes --
expect_status 2 "an invalid FLEET_AUTOMATIC_INSTALL should exit 2"
expect 'len(R) == 0' "an invalid FLEET_AUTOMATIC_INSTALL should make no requests"

mark
run_helper AWS_ACCESS_KEY_ID=ASIAEXAMPLEKEY000000 AWS_SECRET_ACCESS_KEY=example-secret-value --
expect_status fail "a temporary key without a session token should fail"
expect_err 'needs AWS_SESSION_TOKEN' "the error should ask for the session token"
expect 'len(R) == 0' "a temporary key without a token should make no requests"

mark
run_helper AWS_ACCESS_KEY_ID=ASIAEXAMPLEKEY000000 AWS_SECRET_ACCESS_KEY=example-secret-value AWS_SESSION_TOKEN=example-token -- --pkg "$PKG" --skip-policies --skip-reports
expect_status ok "a temporary key with a session token should succeed"
expect_err 'expires' "temporary credentials should come with an expiry warning"
expect 'any("FLEET_SECRET_BEACON_AWS_SESSION_TOKEN" in [s["name"] for s in body(r)["secrets"]] for r in req("PUT", "/api/latest/fleet/spec/secret_variables"))' \
  "the session token should be stored as a secret variable"
stop_fake

# --- Dry runs make no network calls ------------------------------------------
start_fake dryrun '{}'
run_helper -- --dry-run
expect_status ok "dry run should succeed without keys or a package"
grep -q 'FLEET_SECRET_BEACON_AWS_ACCESS_KEY_ID' "$CASE_DIR/out" || fail "dry run should preview the secret placeholders"
run_helper -- --dry-run --skip-software
expect_status ok "the documented dry run should succeed"
expect 'len(R) == 0' "dry runs should not call Fleet or download the package"
stop_fake

# --- Roles and errors ----------------------------------------------------------
# /version answers 200 first, so this also checks that a refused call made inside
# $(...) is seen as refused rather than inheriting the earlier status.
start_fake badtoken '{"overrides": {"GET /api/latest/fleet/me": [401, {"message": "Authentication required"}]}}'
run_helper $KEYS -- --pkg "$PKG"
expect_status fail "a rejected token should stop the helper"
expect_err 'rejected the API token' "a 401 should say the token was rejected"
expect 'len(writes()) == 0' "a rejected token should stop before any write"
stop_fake

start_fake secrets403 '{
  "me": {"user": {"name": "M", "email": "m@example.com", "global_role": "maintainer", "teams": []}},
  "overrides": {"PUT /api/latest/fleet/spec/secret_variables": [403, {"message": "forbidden"}]}
}'
run_helper $KEYS -- --pkg "$PKG"
expect_status fail "a refused secret write should fail"
expect_err 'global admin, maintainer, or GitOps' "a refused secret write should explain the global role"
if grep -q 'Custom software packages' "$CASE_DIR/err"; then
  fail "a refused secret write should not blame software packages"
fi
stop_fake

start_fake teamrole '{"me": {"user": {"name": "T", "email": "t@example.com", "global_role": null, "teams": [{"id": 7, "role": "maintainer"}]}}}'
run_helper $KEYS -- --pkg "$PKG"
expect_status fail "a team maintainer token cannot store secrets"
expect_err 'cannot write Fleet secret variables' "the preflight should name the secret-variable limit"
expect 'len(writes()) == 0' "the role preflight should stop before any write"
mark
run_helper BEACON_AWS_CREDENTIAL_MODE=none -- --pkg "$PKG" --skip-reports
expect_status ok "a team maintainer can run the helper when it stores no secrets"
stop_fake

start_fake norole '{"me": {"user": {"name": "O", "email": "o@example.com", "global_role": "observer", "teams": []}}}'
run_helper BEACON_AWS_CREDENTIAL_MODE=none -- --pkg "$PKG"
expect_status fail "an observer token should be refused"
expect_err 'cannot manage team 7' "the preflight should name the team"
expect 'len(writes()) == 0' "the team preflight should stop before any write"
stop_fake

start_fake multipkg '{
  "titles": [{"id": 42, "name": "endpoint", "bundle_identifier": "ai.asymptote.beacon.endpoint",
    "software_package": {"name": "BeaconEndpointAgent-1.0.0-arm64.pkg", "hash_sha256": "a"},
    "detail_packages": [{"name": "BeaconEndpointAgent-1.0.0-arm64.pkg"}, {"name": "BeaconEndpointAgent-1.1.0-arm64.pkg"}]}]
}'
run_helper $KEYS -- --pkg "$PKG" --skip-scripts
expect_status fail "a title with two packages should stop the helper"
expect_err 'has 2 packages' "the error should say how many packages the title has"
expect 'not any(r["path"].startswith("/api/latest/fleet/software/") for r in writes())' \
  "the helper should not guess which package to update"
stop_fake

echo "Fleet helper checks passed"
