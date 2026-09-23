#!/bin/sh
# Behaviour checks for the Fleet osquery files in packaging/macos/fleet. osquery
# is not available in CI, so each file runs in sqlite3 (the engine osquery is
# built on) against stub tables whose columns come from the osquery table specs.
# The stub launchd table has no pid column, like the real one: a query that
# reads launchd.pid fails here the same way it fails on a host.
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../../.." && pwd)"
SQL_DIR="$ROOT_DIR/packaging/macos/fleet"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

command -v sqlite3 >/dev/null 2>&1 || { echo "sqlite3 is required for the Fleet SQL checks" >&2; exit 1; }

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

NOW="$(date +%s)"

cat >"$TMP_DIR/schema.sql" <<'EOF'
CREATE TABLE system_info (hostname TEXT, uuid TEXT, cpu_type TEXT, cpu_subtype TEXT, cpu_brand TEXT, hardware_model TEXT);
CREATE TABLE package_receipts (package_id TEXT, package_filename TEXT, version TEXT, location TEXT, install_time DOUBLE, installer_name TEXT, path TEXT);
CREATE TABLE file (path TEXT, directory TEXT, filename TEXT, inode BIGINT, uid BIGINT, gid BIGINT, mode TEXT, size BIGINT, atime BIGINT, mtime BIGINT, ctime BIGINT, type TEXT);
CREATE TABLE processes (pid BIGINT, name TEXT, path TEXT, cmdline TEXT, state TEXT, uid BIGINT, gid BIGINT, parent BIGINT);
CREATE TABLE launchd (path TEXT, name TEXT, label TEXT, program TEXT, run_at_load TEXT, keep_alive TEXT, on_demand TEXT, disabled TEXT, username TEXT, groupname TEXT, stdout_path TEXT, stderr_path TEXT, start_interval TEXT, program_arguments TEXT, watch_paths TEXT, queue_directories TEXT, inetd_compatibility TEXT, start_on_mount TEXT, root_directory TEXT, working_directory TEXT, process_type TEXT);
CREATE TABLE yara (path TEXT, matches TEXT, count INTEGER, sig_group TEXT, sigfile TEXT, sigrule TEXT, strings TEXT, tags TEXT);
EOF

# host NAME CPU: start a fixture for one host.
host() {
  cp "$TMP_DIR/schema.sql" "$TMP_DIR/$1.sql"
  printf "INSERT INTO system_info (hostname, cpu_type) VALUES ('%s', '%s');\n" "$1" "$2" >>"$TMP_DIR/$1.sql"
}

# healthy NAME HEARTBEAT_AGE COLLECTOR_UID: a Mac with Beacon and S3 forwarding set up.
healthy() {
  cat >>"$TMP_DIR/$1.sql" <<EOF
INSERT INTO package_receipts (package_id, version) VALUES ('ai.asymptote.beacon.endpoint', '1.3.22');
INSERT INTO file (path, size, mtime) VALUES ('/opt/beacon/bin/beacon', 1000, $NOW);
INSERT INTO file (path, size, mtime) VALUES ('/Library/LaunchDaemons/com.beacon.endpoint.collector.plist', 500, $NOW);
INSERT INTO file (path, size, mtime) VALUES ('/Library/LaunchDaemons/com.beacon.endpoint.s3-forwarder.plist', 500, $NOW);
INSERT INTO file (path, size, mtime) VALUES ('/Library/Application Support/Beacon/Forwarders/s3-vector.env', 200, $NOW);
INSERT INTO file (path, size, mtime) VALUES ('/Library/Application Support/Beacon/Forwarders/s3-vector.toml', 900, $NOW);
INSERT INTO file (path, size, mtime) VALUES ('/var/log/beacon-agent/inventory_state.jsonl', 4000, $((NOW - $2)));
INSERT INTO processes (pid, name, path, cmdline, uid) VALUES (101, 'beacon-otelcol', '/opt/beacon/bin/beacon-otelcol', '/opt/beacon/bin/beacon-otelcol --config /Library/Application Support/Beacon/Endpoint/otelcol.yaml', $3);
INSERT INTO processes (pid, name, path, cmdline, uid) VALUES (102, 'vector', '/opt/beacon/bin/vector', '/opt/beacon/bin/vector --config /Library/Application Support/Beacon/Forwarders/s3-vector.toml', 0);
EOF
}

# run HOST FILE: print the rows FILE returns on HOST, failing on any SQL error.
run() {
  { cat "$TMP_DIR/$1.sql"; cat "$SQL_DIR/$2"; } | sqlite3 -bail :memory: 2>"$TMP_DIR/err" || {
    cat "$TMP_DIR/err" >&2
    fail "$2 errored on host $1"
  }
}

# expect_policy HOST FILE pass|fail
expect_policy() {
  got="$(run "$1" "policies/$2")"
  case "$3" in
    pass) [ "$got" = "1" ] || fail "policies/$2 should pass on $1, returned '$got'" ;;
    fail) [ -z "$got" ] || fail "policies/$2 should fail on $1, returned '$got'" ;;
  esac
}

expect_report() {
  got="$(run "$1" "queries/$2")"
  [ "$got" = "$3" ] || fail "queries/$2 on $1 returned '$got', want '$3'"
}

host healthy arm64e
healthy healthy 60 0
host empty arm64e
host intel x86_64h
host stale arm64e
healthy stale 400000 0
host usercollector arm64e
healthy usercollector 60 501

for policy in "$SQL_DIR"/policies/*.sql; do
  name="$(basename "$policy")"
  expect_policy healthy "$name" pass
  expect_policy empty "$name" fail
  expect_policy intel "$name" pass
done
expect_policy stale inventory-heartbeat-recent.sql fail
expect_policy stale collector-running.sql pass
expect_policy usercollector collector-running.sql fail

expect_report healthy beacon-version.sql 1.3.22
expect_report empty beacon-version.sql not_installed
expect_report healthy collector-service-health.sql running
expect_report usercollector collector-service-health.sql not_running
expect_report empty collector-service-health.sql not_installed
expect_report healthy s3-vector-forwarder-health.sql running
expect_report empty s3-vector-forwarder-health.sql not_configured
expect_report empty falcon-vector-forwarder-health.sql not_configured
expect_report empty inventory-heartbeat-age-seconds.sql missing
age="$(run healthy queries/inventory-heartbeat-age-seconds.sql)"
case "$age" in
  ''|*[!0-9]*) fail "inventory heartbeat age should be a number of seconds, got '$age'" ;;
esac
[ "$age" -ge 60 ] && [ "$age" -lt 120 ] || fail "inventory heartbeat age should be about 60s, got $age"

# A report returns exactly one row per host, whatever state the host is in.
for query in "$SQL_DIR"/queries/*.sql; do
  name="$(basename "$query")"
  for h in empty healthy; do
    rows="$(run "$h" "queries/$name" | wc -l | tr -d ' ')"
    [ "$rows" = "1" ] || fail "queries/$name should return one row on $h, returned $rows"
  done
done

echo "Fleet SQL checks passed"
