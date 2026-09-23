#!/usr/bin/env bash
# Configure a Fleet team to install the latest Beacon macOS package, enable
# endpoint self-updates, and forward runtime/inventory JSONL to a customer S3
# bucket through the Vector helper that ships in the package.
#
# This is an admin-side helper. Run it once from a workstation that can reach
# both GitHub Releases and your Fleet server. It does not run on managed Macs.
#
# Fleet custom software packages are a Premium feature and cannot be added to
# "All teams". A failing Fleet post-install script uninstalls the package, so
# this helper does not put S3 or self-update enablement in post-install.
#
# Fleet 4.82 renamed teams to fleets and queries to reports. This helper sends
# the older names (team_id, /teams, /queries), which every Fleet version from
# 4.62 on accepts; newer servers treat them as aliases.
#
# Usage:
#   ./configure-beacon-macos-s3.sh
#   FLEET_URL=https://fleet.example FLEET_TOKEN=... FLEET_TEAM_ID=1 ./configure-beacon-macos-s3.sh --yes
#   ./configure-beacon-macos-s3.sh --dry-run
#
# Docs: https://docs.asymptotelabs.ai/guides/fleet-s3-mdm
# Fleet software: https://fleetdm.com/guides/deploy-software-packages

set -euo pipefail

SCRIPT_NAME="$(basename "$0")"
MANIFEST_URL="${BEACON_UPDATE_MANIFEST_URL:-https://github.com/asymptote-labs/agent-beacon/releases/latest/download/update-manifest.json}"
PKG_IDENTIFIER="${BEACON_PKG_IDENTIFIER:-ai.asymptote.beacon.endpoint}"
# The one place the team parameter is named. Fleet accepts team_id before and
# after its 4.82 rename; switch this to fleet_id if Fleet drops the alias.
FLEET_TEAM_PARAM="${BEACON_FLEET_TEAM_PARAM:-team_id}"
SOFTWARE_DISPLAY_NAME="Beacon Endpoint Agent"
# The package is Apple Silicon only. osquery reports arm64e on Apple Silicon.
PRE_INSTALL_QUERY="SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%';"
ENABLE_UPDATES_SCRIPT_NAME="beacon-enable-self-updates.sh"
CONFIGURE_S3_SCRIPT_NAME="beacon-configure-s3-forwarding.sh"
VALIDATE_SCRIPT_NAME="beacon-validate.sh"
SECRET_ACCESS_KEY_NAME="FLEET_SECRET_BEACON_AWS_ACCESS_KEY_ID"
SECRET_SECRET_KEY_NAME="FLEET_SECRET_BEACON_AWS_SECRET_ACCESS_KEY"
SECRET_SESSION_TOKEN_NAME="FLEET_SECRET_BEACON_AWS_SESSION_TOKEN"
INSTALLED_POLICY_NAME="Beacon: installed"
PAGE_SIZE=100
MAX_PAGES=50

ASSUME_YES=0
DRY_RUN=0
SKIP_DOWNLOAD=0
SKIP_SOFTWARE=0
SKIP_SCRIPTS=0
SKIP_POLICIES=0
SKIP_REPORTS=0
LOCAL_PKG=""
UPDATE_MODE="auto"

usage() {
  cat <<EOF
Configure Fleet to install Beacon on Apple Silicon Macs, enable self-updates,
and start Vector forwarding to S3.

Usage: $SCRIPT_NAME [options]

Options:
  -y, --yes              Use environment variables; only prompt for missing values
      --dry-run          Print the plan and generated host scripts; make no network calls
      --pkg PATH         Use an already-downloaded Beacon .pkg instead of GitHub
      --skip-download    Alias for --pkg when BEACON_PKG is set
      --skip-software    Do not upload the .pkg (scripts/secrets/policies/reports only)
      --skip-scripts     Do not create Fleet host scripts or secret variables
      --skip-policies    Do not create Beacon health policies
      --skip-reports     Do not create Beacon saved queries (reports)
      --skip-queries     Alias for --skip-reports
      --check-only       Enable Beacon self-update in check-only mode
  -h, --help             Show this help

Environment:
  FLEET_URL, FLEET_TOKEN, FLEET_TEAM_ID
  BEACON_S3_BUCKET, AWS_REGION, BEACON_S3_PREFIX, BEACON_S3_STORAGE_CLASS
  AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN
  BEACON_AWS_CREDENTIAL_MODE=keys|none   none: deliver AWS credentials another way
  BEACON_PKG, BEACON_UPDATE_MANIFEST_URL
  FLEET_AUTOMATIC_INSTALL=true|false   default false (pilot first)

The API token needs an admin or maintainer role on the team. Storing AWS keys
as Fleet secret variables needs a global admin or maintainer; team roles cannot
write them.
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    -y|--yes) ASSUME_YES=1 ;;
    --dry-run) DRY_RUN=1 ;;
    --pkg)
      LOCAL_PKG="${2:-}"
      SKIP_DOWNLOAD=1
      shift
      ;;
    --skip-download) SKIP_DOWNLOAD=1 ;;
    --skip-software) SKIP_SOFTWARE=1 ;;
    --skip-scripts) SKIP_SCRIPTS=1 ;;
    --skip-policies) SKIP_POLICIES=1 ;;
    --skip-reports|--skip-queries) SKIP_REPORTS=1 ;;
    --check-only) UPDATE_MODE="check-only" ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown option: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if [ -n "${BEACON_PKG:-}" ] && [ -z "$LOCAL_PKG" ]; then
  LOCAL_PKG="$BEACON_PKG"
  SKIP_DOWNLOAD=1
fi

need_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "Required command not found: $1" >&2
    exit 1
  fi
}

need_cmd curl
need_cmd python3

if ! command -v shasum >/dev/null 2>&1 && ! command -v sha256sum >/dev/null 2>&1; then
  echo "Need shasum or sha256sum to verify the Beacon package" >&2
  exit 1
fi

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/beacon-fleet-XXXXXX")"
trap 'rm -rf "$WORK_DIR"' EXIT INT TERM

json_get() {
  python3 -c '
import json, sys
raw = sys.stdin.read()
if not raw.strip():
    sys.exit(0)
data = json.loads(raw)
cur = data
for part in sys.argv[1:]:
    if cur is None:
        break
    if isinstance(cur, list):
        try:
            cur = cur[int(part)]
        except (ValueError, IndexError):
            cur = None
            break
    elif isinstance(cur, dict):
        cur = cur.get(part)
    else:
        cur = None
        break
if cur is None:
    sys.exit(0)
if isinstance(cur, (dict, list)):
    json.dump(cur, sys.stdout)
elif isinstance(cur, bool):
    sys.stdout.write("true" if cur else "false")
else:
    sys.stdout.write(str(cur))
' "$@"
}

json_quote() {
  python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$1"
}

sha256_file() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    sha256sum "$1" | awk '{print $1}'
  fi
}

prompt_value() {
  local var="$1"
  local message="$2"
  local default="${3:-}"
  local secret="${4:-0}"
  local current="${!var-}"
  local reply=""

  if [ -n "$current" ]; then
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    if [ -n "$default" ]; then
      printf -v "$var" '%s' "$default"
      return 0
    fi
    echo "Missing required value for $var (non-interactive mode)" >&2
    exit 1
  fi

  if [ "$secret" = 1 ]; then
    if [ -n "$default" ]; then
      printf '%s [%s]: ' "$message" "$default" >&2
    else
      printf '%s: ' "$message" >&2
    fi
    IFS= read -r -s reply
    printf '\n' >&2
  else
    if [ -n "$default" ]; then
      printf '%s [%s]: ' "$message" "$default" >&2
    else
      printf '%s: ' "$message" >&2
    fi
    IFS= read -r reply
  fi
  if [ -z "$reply" ]; then
    reply="$default"
  fi
  printf -v "$var" '%s' "$reply"
}

prompt_secret_confirm() {
  local var="$1"
  local message="$2"
  local first="" confirm=""
  if [ -n "${!var-}" ]; then
    return 0
  fi
  if [ "$ASSUME_YES" -eq 1 ]; then
    echo "Missing required secret $var (non-interactive mode)" >&2
    exit 1
  fi
  printf '%s: ' "$message" >&2
  IFS= read -r -s first
  printf '\n' >&2
  printf 'Confirm %s: ' "$message" >&2
  IFS= read -r -s confirm
  printf '\n' >&2
  if [ "$first" != "$confirm" ]; then
    echo "Values did not match" >&2
    exit 1
  fi
  printf -v "$var" '%s' "$first"
}

yes_no() {
  local message="$1"
  local default="${2:-n}"
  local reply=""
  if [ "$ASSUME_YES" -eq 1 ]; then
    [ "$default" = "y" ]
    return
  fi
  printf '%s [%s]: ' "$message" "$default" >&2
  IFS= read -r reply
  reply="${reply:-$default}"
  case "$reply" in
    y|Y|yes|YES|true|TRUE|1) return 0 ;;
    *) return 1 ;;
  esac
}

# SQL for the Beacon policies and reports. These are exact copies of
# packaging/macos/fleet/{policies,queries}/*.sql, embedded because this helper is
# downloaded on its own; a test fails if they drift.
beacon_sql() {
  case "$1" in
    policies/beacon-installed)
      cat <<'EOF'
SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR (
    EXISTS (SELECT 1 FROM package_receipts WHERE package_id = 'ai.asymptote.beacon.endpoint')
    AND EXISTS (SELECT 1 FROM file WHERE path = '/opt/beacon/bin/beacon')
  );
EOF
      ;;
    policies/collector-running)
      cat <<'EOF'
SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR EXISTS (SELECT 1 FROM processes WHERE path = '/opt/beacon/bin/beacon-otelcol' AND uid = 0);
EOF
      ;;
    policies/s3-forwarder-running)
      cat <<'EOF'
SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR EXISTS (
    SELECT 1 FROM processes
    WHERE path = '/opt/beacon/bin/vector'
      AND cmdline LIKE '%/Beacon/Forwarders/s3-vector.toml%'
  );
EOF
      ;;
    policies/s3-forwarding-configured)
      cat <<'EOF'
SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR (
    EXISTS (SELECT 1 FROM file WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.env' AND size > 0)
    AND EXISTS (SELECT 1 FROM file WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.toml')
    AND EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.s3-forwarder.plist')
  );
EOF
      ;;
    policies/inventory-heartbeat-recent)
      cat <<'EOF'
SELECT 1 WHERE
  NOT EXISTS (SELECT 1 FROM system_info WHERE cpu_type LIKE 'arm64%')
  OR EXISTS (
    SELECT 1 FROM file
    WHERE path = '/var/log/beacon-agent/inventory_state.jsonl'
      AND mtime >= CAST(strftime('%s', 'now') AS INTEGER) - 345600
  );
EOF
      ;;
    queries/beacon-version)
      cat <<'EOF'
SELECT COALESCE(
  (SELECT version FROM package_receipts WHERE package_id = 'ai.asymptote.beacon.endpoint'),
  CASE
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/opt/beacon/bin/beacon') THEN 'installed_without_receipt'
    ELSE 'not_installed'
  END
) AS beacon_version;
EOF
      ;;
    queries/collector-service-health)
      cat <<'EOF'
SELECT
  CASE
    WHEN EXISTS (SELECT 1 FROM processes WHERE path = '/opt/beacon/bin/beacon-otelcol' AND uid = 0) THEN 'running'
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.collector.plist') THEN 'not_running'
    ELSE 'not_installed'
  END AS collector_service_health;
EOF
      ;;
    queries/s3-vector-forwarder-health)
      cat <<'EOF'
SELECT
  CASE
    WHEN EXISTS (
      SELECT 1 FROM processes
      WHERE path = '/opt/beacon/bin/vector'
        AND cmdline LIKE '%/Beacon/Forwarders/s3-vector.toml%'
    ) THEN 'running'
    WHEN EXISTS (SELECT 1 FROM file WHERE path = '/Library/LaunchDaemons/com.beacon.endpoint.s3-forwarder.plist') THEN 'not_running'
    ELSE 'not_configured'
  END AS s3_vector_forwarder_health;
EOF
      ;;
    queries/s3-vector-forwarding-configured)
      cat <<'EOF'
SELECT
  CASE
    WHEN COUNT(*) = 0 THEN 'not_configured'
    ELSE 'configured'
  END AS s3_vector_forwarding_state
FROM file
WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.env';
EOF
      ;;
    queries/inventory-heartbeat-age-seconds)
      cat <<'EOF'
SELECT COALESCE(
  (SELECT CAST(CAST(strftime('%s', 'now') AS INTEGER) - mtime AS TEXT)
     FROM file
     WHERE path = '/var/log/beacon-agent/inventory_state.jsonl'),
  'missing'
) AS inventory_heartbeat_age_seconds;
EOF
      ;;
    *)
      echo "Unknown Beacon SQL: $1" >&2
      return 1
      ;;
  esac
}

# Policies: name|sql|description|resolution. Each returns a row only when the
# host is healthy, and passes on Intel Macs, where the package does not apply.
BEACON_POLICIES=(
  "${INSTALLED_POLICY_NAME}|policies/beacon-installed|The Beacon package receipt and /opt/beacon/bin/beacon are present.|Install the Beacon Endpoint Agent software package on this host."
  "Beacon: collector running|policies/collector-running|The Beacon collector (beacon-otelcol) is running as root.|Run /opt/beacon/fleet/scripts/repair.sh, or reinstall the Beacon package."
  "Beacon: S3 forwarder running|policies/s3-forwarder-running|The Vector S3 forwarder is running with the Beacon S3 config.|Run the ${CONFIGURE_S3_SCRIPT_NAME} script, then check /tmp/com.beacon.endpoint.s3-forwarder.err."
  "Beacon: S3 forwarding configured|policies/s3-forwarding-configured|The S3 forwarder env file, Vector config, and LaunchDaemon are present.|Run the ${CONFIGURE_S3_SCRIPT_NAME} script on this host."
  "Beacon: inventory heartbeat within 4 days|policies/inventory-heartbeat-recent|The scheduled inventory job wrote a heartbeat in the last 96 hours. It runs every 6 hours while the Mac is awake.|Run sudo launchctl kickstart system/com.beacon.endpoint.inventory, then check sudo /opt/beacon/bin/beacon endpoint status --system."
)

# Reports (saved queries): name|sql|description. Each returns one row per host.
BEACON_REPORTS=(
  "Beacon install state|queries/beacon-version|Installed Beacon package version, or not_installed."
  "Beacon collector health|queries/collector-service-health|Whether the Beacon collector process is running."
  "Beacon S3 forwarder health|queries/s3-vector-forwarder-health|Whether the Vector S3 forwarder process is running."
  "Beacon S3 forwarding configured|queries/s3-vector-forwarding-configured|Whether the Vector S3 environment file exists."
  "Beacon inventory heartbeat age|queries/inventory-heartbeat-age-seconds|Seconds since the scheduled inventory job last wrote a heartbeat."
)

# fleet_request also records the status in a file, because callers often run it
# inside $(...), where a variable set in the subshell never reaches the caller.
FLEET_HTTP_STATUS=""
fleet_request() {
  local method="$1"
  local path="$2"
  shift 2
  local body="$WORK_DIR/http-body"
  local code=""
  : >"$body"
  code="$(
    curl -sS -o "$body" -w '%{http_code}' \
      --max-time 600 \
      -X "$method" \
      -H "Authorization: Bearer ${FLEET_TOKEN}" \
      -H "Accept: application/json" \
      "$@" \
      "${FLEET_URL}${path}"
  )"
  FLEET_HTTP_STATUS="$code"
  printf '%s' "$code" >"$WORK_DIR/http-status"
  cat "$body"
}

last_status() {
  FLEET_HTTP_STATUS="$(cat "$WORK_DIR/http-status" 2>/dev/null || true)"
}

fleet_json() {
  local method="$1"
  local path="$2"
  shift 2
  fleet_request "$method" "$path" -H "Content-Type: application/json" "$@"
}

print_fleet_error_body() {
  if [ -s "$WORK_DIR/http-body" ]; then
    python3 -c '
import json,sys
raw=sys.stdin.read()
try:
    data=json.loads(raw)
except Exception:
    sys.stdout.write(raw)
    raise SystemExit
msg=data.get("message") or data.get("error") or data.get("errors") or data
if isinstance(msg, (dict, list)):
    json.dump(msg, sys.stdout, indent=2)
    sys.stdout.write("\n")
else:
    print(msg)
' <"$WORK_DIR/http-body" >&2 || cat "$WORK_DIR/http-body" >&2
  fi
}

# require_ok ACTION [HINT]: exit unless the last Fleet call succeeded. HINT is
# printed for 402/403, where the cause depends on which call was refused.
require_ok() {
  local action="$1"
  local hint="${2:-}"
  last_status
  case "$FLEET_HTTP_STATUS" in
    2*) return 0 ;;
  esac
  echo "$action failed (HTTP $FLEET_HTTP_STATUS)" >&2
  print_fleet_error_body
  case "$FLEET_HTTP_STATUS" in
    401)
      echo "Fleet rejected the API token. Check that it is correct and has not expired." >&2
      ;;
    402|403)
      if [ -n "$hint" ]; then
        echo "$hint" >&2
      fi
      ;;
  esac
  exit 1
}

team_query() {
  printf '%s=%s' "$FLEET_TEAM_PARAM" "$FLEET_TEAM_ID"
}

# fleet_list_all PATH KEYS HINT: fetch every page of a Fleet list endpoint and
# print the combined items as one JSON array. KEYS is a |-separated list of
# response keys to read items from (Fleet 4.82 renamed some of them). Paging
# stops at meta.has_next_results, or at a short page when there is no meta.
fleet_list_all() {
  local path="$1"
  local keys="$2"
  local hint="${3:-}"
  local page=0 sep="?" next=""
  local acc="$WORK_DIR/list-acc.json"
  case "$path" in
    *\?*) sep="&" ;;
  esac
  printf '[]' >"$acc"
  while [ "$page" -lt "$MAX_PAGES" ]; do
    fleet_json GET "${path}${sep}page=${page}&per_page=${PAGE_SIZE}" >"$WORK_DIR/list-page.json"
    require_ok "Listing ${path%%\?*}" "$hint"
    next="$(
      python3 -c '
import json, sys
keys = sys.argv[1].split("|")
page_size = int(sys.argv[2])
acc_path, page_path = sys.argv[3], sys.argv[4]
data = json.loads(open(page_path).read() or "{}")
items = []
for key in keys:
    if isinstance(data.get(key), list):
        items = data[key]
        break
acc = json.load(open(acc_path))
acc.extend(items)
json.dump(acc, open(acc_path, "w"))
meta = data.get("meta")
if isinstance(meta, dict) and "has_next_results" in meta:
    print("yes" if meta.get("has_next_results") else "no")
else:
    print("yes" if len(items) >= page_size else "no")
' "$keys" "$PAGE_SIZE" "$acc" "$WORK_DIR/list-page.json"
    )"
    [ "$next" = "yes" ] || break
    page=$((page + 1))
  done
  cat "$acc"
}

echo
echo "Beacon Fleet + S3 setup"
echo "======================="
echo "This helper uploads the latest Apple Silicon Beacon .pkg to one Fleet"
echo "team, stores S3 credentials as Fleet secret variables, creates host"
echo "scripts that enable self-updates and Vector forwarding after install, and"
echo "adds Beacon health policies and reports."
echo

# Everything up to the plan is local: prompts and validation only. No Fleet or
# GitHub call happens until every input is known to be usable.

if [ "$DRY_RUN" -eq 0 ] || [ -n "${FLEET_URL:-}" ]; then
  prompt_value FLEET_URL "Fleet URL (https://fleet.example.com)"
fi
FLEET_URL="${FLEET_URL:-https://fleet.example.com}"
FLEET_URL="${FLEET_URL%/}"
case "$FLEET_URL" in
  https://*|http://*) ;;
  *)
    echo "FLEET_URL must start with http:// or https://" >&2
    exit 1
    ;;
esac

if [ "$DRY_RUN" -eq 0 ]; then
  prompt_value FLEET_TOKEN "Fleet API token" "" 1
  if [ -z "$FLEET_TOKEN" ]; then
    echo "A Fleet API token is required" >&2
    exit 1
  fi
else
  FLEET_TOKEN="${FLEET_TOKEN:-dry-run}"
fi

if [ -z "${FLEET_TEAM_ID:-}" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    FLEET_TEAM_ID="1"
  elif [ "$ASSUME_YES" -eq 1 ]; then
    echo "FLEET_TEAM_ID is required. Custom packages cannot be added to All teams." >&2
    exit 1
  fi
fi
if [ -n "${FLEET_TEAM_ID:-}" ] && ! printf '%s' "$FLEET_TEAM_ID" | grep -Eq '^[0-9]+$'; then
  echo "FLEET_TEAM_ID must be a number. Fleet cannot add custom packages to All teams." >&2
  exit 1
fi

case "${FLEET_AUTOMATIC_INSTALL:-}" in
  true|false) ;;
  "")
    if yes_no "Install Beacon automatically on every Apple Silicon Mac in this team? Start with a pilot team" n; then
      FLEET_AUTOMATIC_INSTALL="true"
    else
      FLEET_AUTOMATIC_INSTALL="false"
    fi
    ;;
  *)
    echo "FLEET_AUTOMATIC_INSTALL must be true or false (got '$FLEET_AUTOMATIC_INSTALL')" >&2
    exit 2
    ;;
esac

echo
prompt_value BEACON_S3_BUCKET "S3 bucket name"
prompt_value AWS_REGION "AWS region" "us-east-1"
prompt_value BEACON_S3_PREFIX "S3 prefix root (do not include runtime/ or inventory/)" "beacon"
prompt_value BEACON_S3_STORAGE_CLASS "S3 storage class" "STANDARD"
BEACON_S3_PREFIX="${BEACON_S3_PREFIX%/}"
case "$BEACON_S3_PREFIX" in
  */runtime) BEACON_S3_PREFIX="${BEACON_S3_PREFIX%/runtime}" ;;
  */inventory) BEACON_S3_PREFIX="${BEACON_S3_PREFIX%/inventory}" ;;
esac
BEACON_S3_PREFIX="${BEACON_S3_PREFIX%/}"
if [ -z "$BEACON_S3_PREFIX" ]; then
  BEACON_S3_PREFIX="beacon"
fi
if [ -z "$BEACON_S3_BUCKET" ]; then
  echo "S3 bucket is required" >&2
  exit 1
fi

echo
echo "Vector on each Mac uses the standard AWS provider chain. Access keys are"
echo "stored as Fleet secret variables (\$FLEET_SECRET_*) and substituted only"
echo "when Fleet sends the script to a host. Fleet still prints script output,"
echo "so the host script never echoes credentials."
echo
CREDENTIAL_MODE=""
case "${BEACON_AWS_CREDENTIAL_MODE:-}" in
  keys|none)
    CREDENTIAL_MODE="$BEACON_AWS_CREDENTIAL_MODE"
    ;;
  "")
    if [ -n "${AWS_ACCESS_KEY_ID:-}" ] && [ -n "${AWS_SECRET_ACCESS_KEY:-}" ]; then
      CREDENTIAL_MODE="keys"
    elif [ "$DRY_RUN" -eq 1 ]; then
      # The preview shows the $FLEET_SECRET_* placeholders; no key is needed.
      CREDENTIAL_MODE="keys"
    elif [ "$ASSUME_YES" -eq 1 ]; then
      echo "AWS credentials are missing. Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY," >&2
      echo "or set BEACON_AWS_CREDENTIAL_MODE=none if you deliver credentials to the Macs another way." >&2
      exit 1
    else
      printf 'AWS credentials: [k]ey pair (recommended) / [n]one, I will deliver them another way [k]: ' >&2
      IFS= read -r CREDENTIAL_MODE || true
      case "${CREDENTIAL_MODE:-k}" in
        k|K|keys) CREDENTIAL_MODE="keys" ;;
        n|N|none) CREDENTIAL_MODE="none" ;;
        *)
          echo "Answer k or n" >&2
          exit 2
          ;;
      esac
    fi
    ;;
  *)
    echo "BEACON_AWS_CREDENTIAL_MODE must be 'keys' or 'none' (got '$BEACON_AWS_CREDENTIAL_MODE')" >&2
    exit 2
    ;;
esac

CREDENTIAL_LABEL="$CREDENTIAL_MODE"
if [ "$CREDENTIAL_MODE" = "keys" ] && [ "$DRY_RUN" -eq 0 ]; then
  prompt_value AWS_ACCESS_KEY_ID "AWS access key ID"
  prompt_secret_confirm AWS_SECRET_ACCESS_KEY "AWS secret access key"
  if [ -z "${AWS_SESSION_TOKEN:-}" ] && [ "$ASSUME_YES" -eq 0 ]; then
    printf 'AWS session token (only for temporary credentials; Enter to skip): ' >&2
    IFS= read -r -s AWS_SESSION_TOKEN || true
    printf '\n' >&2
  fi
  if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
    echo "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required for key-pair mode" >&2
    exit 1
  fi
  case "$AWS_ACCESS_KEY_ID" in
    ASIA*)
      if [ -z "${AWS_SESSION_TOKEN:-}" ]; then
        echo "AWS_ACCESS_KEY_ID starts with ASIA, so it is a temporary key and needs AWS_SESSION_TOKEN." >&2
        echo "Use a long-lived IAM user key (AKIA...) for the S3 writer instead." >&2
        exit 1
      fi
      ;;
    AKIA*)
      if [ -n "${AWS_SESSION_TOKEN:-}" ]; then
        echo "Warning: AWS_SESSION_TOKEN is set but AWS_ACCESS_KEY_ID is a long-lived key (AKIA...)." >&2
        echo "Long-lived keys do not use a session token; check that the values belong together." >&2
      fi
      ;;
  esac
  if [ -n "${AWS_SESSION_TOKEN:-}" ]; then
    CREDENTIAL_LABEL="keys (temporary, expires)"
    echo >&2
    echo "Warning: these are temporary AWS credentials. Each Mac stores them once, when the" >&2
    echo "S3 script runs. When the session token expires, S3 rejects the uploads and Vector" >&2
    echo "drops those batches until you rerun this helper and the S3 script with new values." >&2
    echo "Use a long-lived IAM user key limited to s3:PutObject for the writer, or set" >&2
    echo "BEACON_AWS_CREDENTIAL_MODE=none and give the Macs credentials that refresh themselves." >&2
    echo >&2
    if [ "$ASSUME_YES" -eq 0 ] && ! yes_no "Continue with temporary credentials?" n; then
      exit 1
    fi
  fi
elif [ "$CREDENTIAL_MODE" = "keys" ] && [ -n "${AWS_SESSION_TOKEN:-}" ]; then
  CREDENTIAL_LABEL="keys (temporary, expires)"
fi

PKG_PATH=""
PKG_VERSION=""
PKG_SHA=""
PKG_PLAN="skipped"
if [ "$SKIP_SOFTWARE" -eq 0 ]; then
  if [ "$SKIP_DOWNLOAD" -eq 1 ]; then
    PKG_PATH="${LOCAL_PKG:-}"
    if [ -z "$PKG_PATH" ] || [ ! -f "$PKG_PATH" ]; then
      echo " --pkg / BEACON_PKG must point at an existing .pkg" >&2
      exit 1
    fi
    PKG_SHA="$(sha256_file "$PKG_PATH")"
    PKG_VERSION="$(basename "$PKG_PATH" | sed -n 's/^BeaconEndpointAgent-\(.*\)-arm64\.pkg$/\1/p')"
    PKG_PLAN="$PKG_PATH"
  else
    PKG_PLAN="latest darwin_arm64 package from $MANIFEST_URL"
  fi
fi

ENABLE_UPDATES_SCRIPT="$WORK_DIR/$ENABLE_UPDATES_SCRIPT_NAME"
if [ "$UPDATE_MODE" = "check-only" ]; then
  ENABLE_FLAG=" --check-only"
else
  ENABLE_FLAG=""
fi
cat >"$ENABLE_UPDATES_SCRIPT" <<EOF
#!/bin/sh
set -eu
# Generated by $SCRIPT_NAME. Runs on the Mac after Beacon is installed.
BEACON="\${BEACON_BIN:-/opt/beacon/bin/beacon}"
if [ ! -x "\$BEACON" ]; then
  echo "Beacon is not installed at \$BEACON" >&2
  exit 1
fi
"\$BEACON" endpoint update enable${ENABLE_FLAG}
"\$BEACON" endpoint update status
EOF

VALIDATE_SCRIPT="$WORK_DIR/$VALIDATE_SCRIPT_NAME"
cat >"$VALIDATE_SCRIPT" <<'EOF'
#!/bin/sh
set -eu
BEACON="${BEACON_BIN:-/opt/beacon/bin/beacon}"
if [ -x /opt/beacon/fleet/scripts/validate.sh ]; then
  /opt/beacon/fleet/scripts/validate.sh
else
  "$BEACON" endpoint status --json
fi
if [ -x "$BEACON" ]; then
  "$BEACON" endpoint update status || true
fi
if command -v launchctl >/dev/null 2>&1; then
  launchctl print system/com.beacon.endpoint.collector | grep -E 'state =|pid =|last exit code' || true
  launchctl print system/com.beacon.endpoint.updater | grep -E 'state =|pid =|last exit code' || true
  launchctl print system/com.beacon.endpoint.s3-forwarder | grep -E 'state =|pid =|last exit code' || true
fi
EOF

S3_SCRIPT="$WORK_DIR/$CONFIGURE_S3_SCRIPT_NAME"
BUCKET_JSON="$(json_quote "$BEACON_S3_BUCKET")"
REGION_JSON="$(json_quote "$AWS_REGION")"
PREFIX_JSON="$(json_quote "$BEACON_S3_PREFIX")"
STORAGE_JSON="$(json_quote "$BEACON_S3_STORAGE_CLASS")"
{
  cat <<EOF
#!/bin/sh
set -eu
# Generated by $SCRIPT_NAME. Fleet substitutes \$FLEET_SECRET_* before this
# script reaches the host. Do not echo credential values; Fleet does not hide
# them in script results.
INSTALLER="\${BEACON_S3_VECTOR_SCRIPT:-/opt/beacon/jamf/claude/s3/install-forwarder.sh}"
VECTOR="\${BEACON_VECTOR_BIN:-/opt/beacon/bin/vector}"
if [ ! -x "\$INSTALLER" ]; then
  echo "S3 forwarder helper missing at \$INSTALLER; install the Beacon package first" >&2
  exit 1
fi
if [ ! -x "\$VECTOR" ]; then
  echo "Vector missing at \$VECTOR; the Beacon package must include /opt/beacon/bin/vector" >&2
  exit 1
fi
export BEACON_S3_BUCKET=$BUCKET_JSON
export AWS_REGION=$REGION_JSON
export BEACON_S3_PREFIX=$PREFIX_JSON
export BEACON_S3_STORAGE_CLASS=$STORAGE_JSON
export BEACON_VECTOR_READ_FROM="\${BEACON_VECTOR_READ_FROM:-end}"
EOF
  if [ "$CREDENTIAL_MODE" = "keys" ]; then
    cat <<EOF
export AWS_ACCESS_KEY_ID="\$$SECRET_ACCESS_KEY_NAME"
export AWS_SECRET_ACCESS_KEY="\$$SECRET_SECRET_KEY_NAME"
EOF
    if [ -n "${AWS_SESSION_TOKEN:-}" ]; then
      cat <<EOF
export AWS_SESSION_TOKEN="\$$SECRET_SESSION_TOKEN_NAME"
EOF
    fi
  fi
  cat <<'EOF'
"$INSTALLER"
echo "S3 Vector forwarder configured. Credential values were not printed."
if command -v launchctl >/dev/null 2>&1; then
  launchctl print system/com.beacon.endpoint.s3-forwarder | grep -E 'state =|pid =|last exit code' || true
fi
EOF
} >"$S3_SCRIPT"

INSTALL_SCRIPT="$WORK_DIR/beacon-install.sh"
cat >"$INSTALL_SCRIPT" <<'EOF'
#!/bin/sh
# Fleet default for .pkg. The Beacon package postinstall performs the system
# endpoint install; do not add S3 or self-update enablement here. If a Fleet
# post-install script fails, fleetd uninstalls the package.
installer -pkg "$INSTALLER_PATH" -target /
EOF

UNINSTALL_SCRIPT="$WORK_DIR/beacon-uninstall.sh"
cat >"$UNINSTALL_SCRIPT" <<EOF
#!/bin/sh
set -eu
# Fleet's default .pkg uninstall only removes .app bundles. Beacon lives under
# /opt/beacon and uses LaunchDaemons, so use the packaged cleanup helper.
if [ -x /opt/beacon/jamf/scripts/full-cleanup.sh ]; then
  /opt/beacon/jamf/scripts/full-cleanup.sh
elif [ -x /opt/beacon/fleet/scripts/uninstall.sh ]; then
  /opt/beacon/fleet/scripts/uninstall.sh
  rm -rf /opt/beacon "/Library/Application Support/Beacon" /Library/LaunchDaemons/com.beacon.endpoint.*.plist
  pkgutil --forget "$PKG_IDENTIFIER" >/dev/null 2>&1 || true
fi
EOF

echo
echo "Plan"
echo "----"
echo "  Fleet:              $FLEET_URL"
echo "  Team ID:            ${FLEET_TEAM_ID:-choose from the list}"
echo "  Package:            $PKG_PLAN"
echo "  Version:            ${PKG_VERSION:-latest}"
echo "  Automatic install:  $FLEET_AUTOMATIC_INSTALL"
echo "  Self-update mode:   $UPDATE_MODE"
echo "  S3 bucket:          $BEACON_S3_BUCKET"
echo "  S3 prefix:          $BEACON_S3_PREFIX/{runtime,inventory}/date=YYYY-MM-DD/"
echo "  AWS region:         $AWS_REGION"
echo "  AWS credentials:    $CREDENTIAL_LABEL"
echo "  Host scripts:       $ENABLE_UPDATES_SCRIPT_NAME, $CONFIGURE_S3_SCRIPT_NAME, $VALIDATE_SCRIPT_NAME"
if [ "$SKIP_POLICIES" -eq 0 ]; then
  echo "  Policies:           ${#BEACON_POLICIES[@]} Beacon health policies"
fi
if [ "$SKIP_REPORTS" -eq 0 ]; then
  echo "  Reports:            ${#BEACON_REPORTS[@]} Beacon saved queries"
fi
if [ "$DRY_RUN" -eq 1 ]; then
  echo
  echo "Dry run: no Fleet or GitHub calls were made. Generated host scripts are in:"
  echo "  $WORK_DIR"
  echo
  echo "----- $CONFIGURE_S3_SCRIPT_NAME -----"
  # Show the S3 script with Fleet secret placeholders, not live key material.
  cat "$S3_SCRIPT"
  trap - EXIT
  exit 0
fi

echo
echo "Checking Fleet..."
fleet_json GET /api/latest/fleet/version >"$WORK_DIR/version.json" || true
if [ "$FLEET_HTTP_STATUS" = "200" ]; then
  FLEET_VERSION="$(json_get version <"$WORK_DIR/version.json")"
  echo "Fleet version: ${FLEET_VERSION:-unknown}"
  if [ -n "$FLEET_VERSION" ] && python3 -c '
import re, sys
m = re.match(r"v?(\d+)\.(\d+)", sys.argv[1])
sys.exit(0 if m and (int(m.group(1)), int(m.group(2))) < (4, 62) else 1)
' "$FLEET_VERSION"; then
    echo "Warning: Fleet $FLEET_VERSION is older than 4.62, which added secret variables. Upgrade Fleet before storing AWS keys." >&2
  fi
fi

me="$(fleet_json GET /api/latest/fleet/me)"
require_ok "Fleet authentication"
me_name="$(printf '%s' "$me" | json_get user name)"
me_email="$(printf '%s' "$me" | json_get user email)"
echo "Authenticated as ${me_name:-unknown}${me_email:+ <$me_email>}"

if [ -z "${FLEET_TEAM_ID:-}" ]; then
  echo
  echo "Available Fleet teams:"
  teams_json="$(fleet_list_all /api/latest/fleet/teams "teams|fleets" "Listing teams needs a role that can read them.")"
  python3 -c '
import json,sys
teams=json.loads(sys.stdin.read() or "[]")
if not teams:
    print("  (none returned; you still need a team ID, not All teams)")
    raise SystemExit
for t in teams:
    print("  {id:>6}  {name}".format(id=t.get("id"), name=t.get("name")))
' <<<"$teams_json"
  prompt_value FLEET_TEAM_ID "Fleet team ID to deploy into"
  if ! printf '%s' "$FLEET_TEAM_ID" | grep -Eq '^[0-9]+$'; then
    echo "FLEET_TEAM_ID must be a number. Fleet cannot add custom packages to All teams." >&2
    exit 1
  fi
fi

# Check the token's roles before writing anything. Only global admins,
# maintainers, and GitOps users can write secret variables; team roles cannot.
SECRETS_NEEDED="none"
if [ "$CREDENTIAL_MODE" = "keys" ] && [ "$SKIP_SCRIPTS" -eq 0 ]; then
  SECRETS_NEEDED="keys"
fi
role_check="$(
  printf '%s' "$me" | python3 -c '
import json, sys
team = int(sys.argv[1])
need_secrets = sys.argv[2] == "keys"
user = (json.loads(sys.stdin.read() or "{}") or {}).get("user") or {}
if "global_role" not in user and not (user.get("teams") or user.get("fleets")):
    print("unknown")
    raise SystemExit
global_role = user.get("global_role")
team_role = None
for t in user.get("teams") or user.get("fleets") or []:
    if t.get("id") == team:
        team_role = t.get("role")
write = {"admin", "maintainer"}
if global_role not in write and team_role not in write:
    print("team")
elif need_secrets and global_role not in {"admin", "maintainer", "gitops"}:
    print("secrets")
else:
    print("ok")
' "$FLEET_TEAM_ID" "$SECRETS_NEEDED"
)"
case "$role_check" in
  ok) ;;
  unknown)
    echo "Warning: Fleet did not report this token's roles; continuing without the role check." >&2
    ;;
  team)
    echo "This API token cannot manage team $FLEET_TEAM_ID. It needs an admin or maintainer role on that team, or globally." >&2
    exit 1
    ;;
  secrets)
    echo "This API token cannot write Fleet secret variables. Only a global admin, maintainer, or GitOps user can; team roles cannot." >&2
    echo "Use an API-only user with the global maintainer role, or rerun with BEACON_AWS_CREDENTIAL_MODE=none" >&2
    echo "and add the BEACON_AWS_* variables under Controls > Variables yourself." >&2
    exit 1
    ;;
esac

if [ "$SKIP_SOFTWARE" -eq 0 ] && [ "$SKIP_DOWNLOAD" -eq 0 ]; then
  echo
  echo "Fetching latest Beacon release manifest..."
  curl -fsSL --max-time 60 "$MANIFEST_URL" >"$WORK_DIR/update-manifest.json"
  PKG_VERSION="$(json_get version <"$WORK_DIR/update-manifest.json")"
  PKG_URL="$(json_get artifacts darwin_arm64 url <"$WORK_DIR/update-manifest.json")"
  PKG_SHA="$(json_get artifacts darwin_arm64 sha256 <"$WORK_DIR/update-manifest.json")"
  if [ -z "$PKG_VERSION" ] || [ -z "$PKG_URL" ] || [ -z "$PKG_SHA" ]; then
    echo "update-manifest.json did not include a darwin_arm64 package" >&2
    cat "$WORK_DIR/update-manifest.json" >&2
    exit 1
  fi
  echo "Latest package: Beacon $PKG_VERSION (Apple Silicon)"
  PKG_PATH="$WORK_DIR/$(basename "$PKG_URL")"
  echo "Downloading $(basename "$PKG_URL")..."
  curl -fL --max-time 600 -o "$PKG_PATH" "$PKG_URL"
  actual_sha="$(sha256_file "$PKG_PATH")"
  if [ "$actual_sha" != "$PKG_SHA" ]; then
    echo "SHA-256 mismatch for $PKG_PATH" >&2
    echo "  expected: $PKG_SHA" >&2
    echo "  actual:   $actual_sha" >&2
    exit 1
  fi
  echo "Checksum OK"
fi

SECRETS_HINT="Fleet secret variables can only be written by a global admin, maintainer, or GitOps user; team roles cannot. Use an API-only user with the global maintainer role, or rerun with BEACON_AWS_CREDENTIAL_MODE=none and add the BEACON_AWS_* variables under Controls > Variables yourself."
SCRIPTS_HINT="Managing scripts on team $FLEET_TEAM_ID needs an admin or maintainer role on that team, or globally. GitOps users can upload scripts but cannot list them, so this helper cannot use a GitOps token."
SOFTWARE_HINT="Custom software packages need Fleet Premium, a specific team (not All teams), and an admin or maintainer role on that team."
POLICIES_HINT="Team policies need an admin or maintainer role on team $FLEET_TEAM_ID, or globally."

if [ "$CREDENTIAL_MODE" = "keys" ] && [ "$SKIP_SCRIPTS" -eq 0 ]; then
  echo
  echo "Storing AWS credentials as Fleet secret variables..."
  ACCESS_NAME="$SECRET_ACCESS_KEY_NAME" \
  SECRET_NAME="$SECRET_SECRET_KEY_NAME" \
  TOKEN_NAME="$SECRET_SESSION_TOKEN_NAME" \
  AWS_ACCESS_KEY_ID="$AWS_ACCESS_KEY_ID" \
  AWS_SECRET_ACCESS_KEY="$AWS_SECRET_ACCESS_KEY" \
  AWS_SESSION_TOKEN="${AWS_SESSION_TOKEN:-}" \
    python3 -c '
import json, os, sys
secrets = [
    {"name": os.environ["ACCESS_NAME"], "value": os.environ["AWS_ACCESS_KEY_ID"]},
    {"name": os.environ["SECRET_NAME"], "value": os.environ["AWS_SECRET_ACCESS_KEY"]},
]
token = os.environ.get("AWS_SESSION_TOKEN") or ""
if token:
    secrets.append({"name": os.environ["TOKEN_NAME"], "value": token})
json.dump({"secrets": secrets}, sys.stdout)
' >"$WORK_DIR/secrets.json"
  fleet_json PUT /api/latest/fleet/spec/secret_variables --data @"$WORK_DIR/secrets.json" >/dev/null
  rm -f "$WORK_DIR/secrets.json"
  if [ "$FLEET_HTTP_STATUS" = "404" ]; then
    echo "This Fleet server has no secret variables API. Fleet added it in 4.62." >&2
    echo "Upgrade Fleet, or rerun with BEACON_AWS_CREDENTIAL_MODE=none and deliver AWS credentials to the Macs another way." >&2
    exit 1
  fi
  require_ok "Storing Fleet secret variables" "$SECRETS_HINT"
  echo "Fleet secret variables stored (values are hidden in the Fleet UI)."
fi

if [ "$SKIP_SCRIPTS" -eq 0 ]; then
  echo
  scripts_json="$(fleet_list_all "/api/latest/fleet/scripts?$(team_query)" "scripts" "$SCRIPTS_HINT")"
  for pair in \
    "$ENABLE_UPDATES_SCRIPT_NAME|$ENABLE_UPDATES_SCRIPT" \
    "$CONFIGURE_S3_SCRIPT_NAME|$S3_SCRIPT" \
    "$VALIDATE_SCRIPT_NAME|$VALIDATE_SCRIPT"; do
    name="${pair%%|*}"
    file="${pair#*|}"
    existing_id="$(
      python3 -c '
import json,sys
name=sys.argv[1]
for s in json.loads(sys.stdin.read() or "[]"):
    if s.get("name")==name:
        print(s.get("id") or "")
        break
' "$name" <<<"$scripts_json"
    )"
    if [ -n "$existing_id" ]; then
      echo "Updating Fleet script $name (id $existing_id)..."
      fleet_request PATCH "/api/latest/fleet/scripts/${existing_id}" \
        -F "script=@${file};filename=${name}" >/dev/null
      require_ok "Updating $name" "$SCRIPTS_HINT"
    else
      echo "Creating Fleet script $name..."
      fleet_request POST /api/latest/fleet/scripts \
        -F "${FLEET_TEAM_PARAM}=${FLEET_TEAM_ID}" \
        -F "script=@${file};filename=${name}" >/dev/null
      require_ok "Creating $name" "$SCRIPTS_HINT"
    fi
  done
fi

# find_beacon_title prints the id of this team's Beacon software title, or
# nothing. Fleet names the title after the last part of the package identifier
# ("endpoint"), so match on the bundle identifier or the package file name.
find_beacon_title() {
  local titles_json
  titles_json="$(fleet_list_all "/api/latest/fleet/software/titles?$(team_query)&available_for_install=true&packages_only=true" "software_titles" "$SOFTWARE_HINT")"
  python3 -c '
import json, re, sys
pkg_id = sys.argv[1]
matches = []
for t in json.loads(sys.stdin.read() or "[]"):
    pkgs = list(t.get("packages") or [])
    if t.get("software_package"):
        pkgs.append(t["software_package"])
    if t.get("bundle_identifier") == pkg_id or any(
        re.fullmatch(r"BeaconEndpointAgent-.+\.pkg", p.get("name") or "") for p in pkgs
    ):
        matches.append(t.get("id"))
if len(matches) > 1:
    sys.stderr.write("More than one software title on this team looks like Beacon: ids %s.\n" % ", ".join(str(m) for m in matches))
    sys.stderr.write("Delete the extra titles in Fleet, then rerun this helper.\n")
    sys.exit(3)
if matches:
    print(matches[0])
' "$PKG_IDENTIFIER" <<<"$titles_json"
}

title_id=""
if [ "$SKIP_SOFTWARE" -eq 0 ]; then
  echo
  echo "Looking for an existing Beacon software title on this team..."
  title_id="$(find_beacon_title)"

  if [ -z "$title_id" ]; then
    echo "Uploading ${PKG_PATH##*/} as a custom package (this can take several minutes)..."
    echo "If you self-host Fleet, the server stores packages in S3 and load-balancer timeouts should be at least 5 minutes."
    fleet_request POST /api/latest/fleet/software/package \
      -F "${FLEET_TEAM_PARAM}=${FLEET_TEAM_ID}" \
      -F "software=@${PKG_PATH}" \
      -F "install_script=<${INSTALL_SCRIPT}" \
      -F "uninstall_script=<${UNINSTALL_SCRIPT}" \
      --form-string "pre_install_query=${PRE_INSTALL_QUERY}" \
      -F "self_service=false" >/dev/null
    if [ "$FLEET_HTTP_STATUS" = "409" ]; then
      # Fleet before 4.90 allows one package per title; another run got there first.
      echo "Fleet already has a Beacon package on this team; updating it instead."
      title_id="$(find_beacon_title)"
      if [ -z "$title_id" ]; then
        require_ok "Uploading Beacon software package" "$SOFTWARE_HINT"
      fi
    else
      require_ok "Uploading Beacon software package" "$SOFTWARE_HINT"
      title_id="$(json_get software_package title_id <"$WORK_DIR/http-body")"
      if [ -z "$title_id" ]; then
        title_id="$(find_beacon_title)"
      fi
      if [ -n "$title_id" ]; then
        # Fleet only accepts display_name when updating a package (4.77 and later).
        fleet_request PATCH "/api/latest/fleet/software/titles/${title_id}/package" \
          -F "${FLEET_TEAM_PARAM}=${FLEET_TEAM_ID}" \
          --form-string "display_name=${SOFTWARE_DISPLAY_NAME}" >/dev/null
        case "$FLEET_HTTP_STATUS" in
          2*) ;;
          *) echo "Warning: could not set the display name (HTTP $FLEET_HTTP_STATUS); Fleet will list the package as \"endpoint\"." >&2 ;;
        esac
      fi
      echo "Beacon software title id: ${title_id:-uploaded}"
      PKG_UPLOADED=1
    fi
  fi

  if [ -n "$title_id" ] && [ -z "${PKG_UPLOADED:-}" ]; then
    fleet_json GET "/api/latest/fleet/software/titles/${title_id}?$(team_query)" >"$WORK_DIR/title.json"
    require_ok "Reading Beacon software title $title_id" "$SOFTWARE_HINT"
    # Print the fields that differ, one per line. A PATCH that changes anything
    # other than self_service cancels pending installs, so send only changes.
    changed="$(
      python3 -c '
import json, sys
title_path, pkg_sha, install_path, uninstall_path, pre_query, display = sys.argv[1:7]
title = (json.load(open(title_path)) or {}).get("software_title") or {}
pkgs = title.get("packages") or []
if len(pkgs) > 1:
    sys.stderr.write("Software title %s has %d packages. Delete all but one in Fleet (Software > the Beacon title), then rerun this helper.\n" % (title.get("id"), len(pkgs)))
    sys.exit(3)
pkg = title.get("software_package") or (pkgs[0] if pkgs else {})
def same(a, b):
    return (a or "").strip() == (b or "").strip()
if (pkg.get("hash_sha256") or "") != pkg_sha:
    print("software")
if not same(pkg.get("install_script"), open(install_path).read()):
    print("install_script")
if not same(pkg.get("uninstall_script"), open(uninstall_path).read()):
    print("uninstall_script")
if not same(pkg.get("pre_install_query"), pre_query):
    print("pre_install_query")
if "display_name" in pkg and not same(pkg.get("display_name"), display):
    print("display_name")
if pkg.get("self_service"):
    print("self_service")
' "$WORK_DIR/title.json" "$PKG_SHA" "$INSTALL_SCRIPT" "$UNINSTALL_SCRIPT" "$PRE_INSTALL_QUERY" "$SOFTWARE_DISPLAY_NAME"
    )"
    if [ -z "$changed" ]; then
      echo "Beacon software title $title_id is already up to date."
    else
      form=(-F "${FLEET_TEAM_PARAM}=${FLEET_TEAM_ID}")
      while IFS= read -r field; do
        case "$field" in
          software) form+=(-F "software=@${PKG_PATH}") ;;
          install_script) form+=(-F "install_script=<${INSTALL_SCRIPT}") ;;
          uninstall_script) form+=(-F "uninstall_script=<${UNINSTALL_SCRIPT}") ;;
          pre_install_query) form+=(--form-string "pre_install_query=${PRE_INSTALL_QUERY}") ;;
          display_name) form+=(--form-string "display_name=${SOFTWARE_DISPLAY_NAME}") ;;
          self_service) form+=(-F "self_service=false") ;;
        esac
      done <<<"$changed"
      echo "Updating software title $title_id: $(printf '%s' "$changed" | tr '\n' ' ')"
      fleet_request PATCH "/api/latest/fleet/software/titles/${title_id}/package" "${form[@]}" >/dev/null
      require_ok "Updating Beacon software package" "$SOFTWARE_HINT"
    fi
    echo "Beacon software title id: $title_id"
  fi
fi

if [ "$SKIP_POLICIES" -eq 0 ]; then
  echo
  if [ -z "$title_id" ]; then
    title_id="$(find_beacon_title)"
  fi
  policies_json="$(fleet_list_all "/api/latest/fleet/teams/${FLEET_TEAM_ID}/policies" "policies" "$POLICIES_HINT")"

  # Fleet's own automatic-install policy for a .pkg checks the apps table for a
  # bundle identifier. Beacon installs no .app, so that policy never passes.
  legacy_ids=""
  if [ -n "$title_id" ]; then
    legacy_ids="$(
      python3 -c '
import json, sys
title_id, pkg_id = int(sys.argv[1]), sys.argv[2]
legacy_query = "SELECT 1 FROM apps WHERE bundle_identifier = %s;" % ("\x27" + pkg_id + "\x27")
for p in json.loads(sys.stdin.read() or "[]"):
    install = p.get("install_software") or {}
    if install.get("software_title_id") == title_id and (p.get("query") or "").strip() == legacy_query:
        print("%s\t%s" % (p.get("id"), p.get("name")))
' "$title_id" "$PKG_IDENTIFIER" <<<"$policies_json"
    )"
  fi

  want_automation="$FLEET_AUTOMATIC_INSTALL"
  if [ -n "$legacy_ids" ]; then
    echo "Fleet's automatic-install policy for Beacon checks for an app bundle that Beacon"
    echo "does not install, so it never passes:"
    printf '%s\n' "$legacy_ids" | sed 's/^/  /'
    echo "\"$INSTALLED_POLICY_NAME\" checks the package receipt instead and will carry the automatic install."
    want_automation="true"
  fi
  if [ "$want_automation" = "true" ] && [ -z "$title_id" ]; then
    echo "Warning: no Beacon software title on this team, so automatic install cannot be attached to \"$INSTALLED_POLICY_NAME\"." >&2
    want_automation="false"
  fi

  for entry in "${BEACON_POLICIES[@]}"; do
    IFS='|' read -r pname psql pdesc presolution <<<"$entry"
    ptitle=""
    if [ "$pname" = "$INSTALLED_POLICY_NAME" ] && [ "$want_automation" = "true" ]; then
      ptitle="$title_id"
    fi
    beacon_sql "$psql" >"$WORK_DIR/policy.sql"
    # Print "create", "same", or "update <id>", and write the request body. A
    # policy edit clears its results and re-arms its automation, so an
    # unchanged policy is left alone.
    action="$(
      NAME="$pname" DESC="$pdesc" RESOLUTION="$presolution" TITLE="$ptitle" \
        python3 -c '
import json, os, sys
policies = json.loads(sys.stdin.read() or "[]")
body_path, sql_path = sys.argv[1], sys.argv[2]
want = {
    "name": os.environ["NAME"],
    "query": open(sql_path).read().strip(),
    "description": os.environ["DESC"],
    "resolution": os.environ["RESOLUTION"],
    "platform": "darwin",
    "critical": False,
}
title = os.environ.get("TITLE") or ""
existing = next((p for p in policies if p.get("name") == want["name"]), None)
body = dict(want)
if title:
    body["software_title_id"] = int(title)
if existing is None:
    json.dump(body, open(body_path, "w"))
    print("create")
    raise SystemExit
differs = any((existing.get(k) or "").strip() != v for k, v in want.items() if isinstance(v, str))
differs = differs or bool(existing.get("critical")) != want["critical"]
current_title = (existing.get("install_software") or {}).get("software_title_id")
if title and current_title != int(title):
    differs = True
elif not title:
    # Never remove an automation someone attached; leave the field unset.
    body.pop("software_title_id", None)
    if current_title:
        sys.stderr.write("Note: \"%s\" already installs software title %s automatically; leaving that in place.\n" % (want["name"], current_title))
json.dump(body, open(body_path, "w"))
print("update %s" % existing.get("id") if differs else "same")
' "$WORK_DIR/policy.json" "$WORK_DIR/policy.sql" <<<"$policies_json"
    )"
    case "$action" in
      create)
        echo "Creating policy \"$pname\"..."
        fleet_json POST "/api/latest/fleet/teams/${FLEET_TEAM_ID}/policies" --data @"$WORK_DIR/policy.json" >/dev/null
        require_ok "Creating policy $pname" "$POLICIES_HINT"
        ;;
      update\ *)
        echo "Updating policy \"$pname\" (id ${action#update })..."
        fleet_json PATCH "/api/latest/fleet/teams/${FLEET_TEAM_ID}/policies/${action#update }" --data @"$WORK_DIR/policy.json" >/dev/null
        require_ok "Updating policy $pname" "$POLICIES_HINT"
        ;;
      same)
        echo "Policy \"$pname\" is already up to date."
        ;;
    esac
  done

  if [ -n "$legacy_ids" ]; then
    if yes_no "Delete the old automatic-install policy now that \"$INSTALLED_POLICY_NAME\" replaces it?" y; then
      ids_json="$(printf '%s\n' "$legacy_ids" | cut -f1 | python3 -c 'import json,sys; print(json.dumps({"ids": [int(l) for l in sys.stdin.read().split()]}))')"
      fleet_json POST "/api/latest/fleet/teams/${FLEET_TEAM_ID}/policies/delete" --data "$ids_json" >/dev/null
      require_ok "Deleting the old automatic-install policy" "$POLICIES_HINT"
      echo "Deleted: $(printf '%s\n' "$legacy_ids" | cut -f2 | tr '\n' ' ')"
    fi
  fi
fi

if [ "$SKIP_REPORTS" -eq 0 ]; then
  echo
  fleet_json GET "/api/latest/fleet/queries?$(team_query)&page=0&per_page=1" >/dev/null
  if [ "$FLEET_HTTP_STATUS" != "200" ]; then
    echo "Skipping reports (could not list saved queries; HTTP $FLEET_HTTP_STATUS)"
  else
    queries_json="$(fleet_list_all "/api/latest/fleet/queries?$(team_query)" "queries|reports")"
    for entry in "${BEACON_REPORTS[@]}"; do
      IFS='|' read -r qname qsql qdesc <<<"$entry"
      existing="$(
        python3 -c '
import json,sys
name=sys.argv[1]
for q in json.loads(sys.stdin.read() or "[]"):
    if q.get("name")==name:
        print(q.get("id") or "")
        break
' "$qname" <<<"$queries_json"
      )"
      beacon_sql "$qsql" >"$WORK_DIR/query.sql"
      NAME="$qname" DESC="$qdesc" TEAM="$FLEET_TEAM_ID" TEAM_PARAM="$FLEET_TEAM_PARAM" \
        python3 -c '
import json,os,sys
json.dump({
  "name": os.environ["NAME"],
  "description": os.environ["DESC"],
  "query": open(sys.argv[1]).read().strip(),
  "platform": "darwin",
  "observer_can_run": True,
  os.environ["TEAM_PARAM"]: int(os.environ["TEAM"]),
}, sys.stdout)
' "$WORK_DIR/query.sql" >"$WORK_DIR/query.json"
      if [ -n "$existing" ]; then
        echo "Updating report $qname (id $existing)..."
        fleet_json PATCH "/api/latest/fleet/queries/${existing}" --data @"$WORK_DIR/query.json" >/dev/null
        require_ok "Updating report $qname"
      else
        echo "Creating report $qname..."
        fleet_json POST /api/latest/fleet/queries --data @"$WORK_DIR/query.json" >/dev/null
        require_ok "Creating report $qname"
      fi
    done
  fi
fi

cat <<EOF

Done. Next steps in Fleet
-------------------------
1. Scope the Beacon Endpoint Agent package to a pilot label of Apple Silicon
   Macs. Custom packages cannot target "All teams". The package's pre-install
   query skips Intel Macs.
2. Confirm fleetd was deployed with scripts enabled (default when using Fleet
   MDM; otherwise --enable-scripts).
3. Install Beacon from Host > Software > Library, or let the "$INSTALLED_POLICY_NAME"
   policy install it if you turned on automatic install.
4. After the package is installed, run these scripts on the same hosts, in order:
     $ENABLE_UPDATES_SCRIPT_NAME
     $CONFIGURE_S3_SCRIPT_NAME
     $VALIDATE_SCRIPT_NAME
   Do not attach S3 setup as the package post-install script: Fleet uninstalls
   the package if post-install fails.
5. Watch the "Beacon:" policies. Each one passes only when that part of Beacon is
   healthy on the host.
6. S3 objects appear under:
     s3://$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/runtime/date=YYYY-MM-DD/
     s3://$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/inventory/date=YYYY-MM-DD/
   Vector batches for up to 5 minutes before the first upload.

IAM for the writer: s3:PutObject on arn:aws:s3:::$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/*
If you use GitOps, re-apply this helper after the next GitOps run or fold the
package, scripts, policies, and \$FLEET_SECRET_* variables into your GitOps repo.
EOF
