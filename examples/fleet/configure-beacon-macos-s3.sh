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
SOFTWARE_QUERY="${BEACON_FLEET_SOFTWARE_QUERY:-Beacon}"
ENABLE_UPDATES_SCRIPT_NAME="beacon-enable-self-updates.sh"
CONFIGURE_S3_SCRIPT_NAME="beacon-configure-s3-forwarding.sh"
VALIDATE_SCRIPT_NAME="beacon-validate.sh"
SECRET_ACCESS_KEY_NAME="FLEET_SECRET_BEACON_AWS_ACCESS_KEY_ID"
SECRET_SECRET_KEY_NAME="FLEET_SECRET_BEACON_AWS_SECRET_ACCESS_KEY"
SECRET_SESSION_TOKEN_NAME="FLEET_SECRET_BEACON_AWS_SESSION_TOKEN"

ASSUME_YES=0
DRY_RUN=0
SKIP_DOWNLOAD=0
SKIP_SOFTWARE=0
SKIP_SCRIPTS=0
SKIP_QUERIES=0
LOCAL_PKG=""
UPDATE_MODE="auto"

usage() {
  cat <<EOF
Configure Fleet to install Beacon on Apple Silicon Macs, enable self-updates,
and start Vector forwarding to S3.

Usage: $SCRIPT_NAME [options]

Options:
  -y, --yes              Use environment variables; only prompt for missing values
      --dry-run          Print the plan and generated host scripts; do not call Fleet
      --pkg PATH         Use an already-downloaded Beacon .pkg instead of GitHub
      --skip-download    Alias for --pkg when BEACON_PKG is set
      --skip-software    Do not upload the .pkg (scripts/secrets/queries only)
      --skip-scripts     Do not create Fleet host scripts
      --skip-queries     Do not create Fleet saved queries
      --check-only       Enable Beacon self-update in check-only mode
  -h, --help             Show this help

Environment:
  FLEET_URL, FLEET_TOKEN, FLEET_TEAM_ID
  BEACON_S3_BUCKET, AWS_REGION, BEACON_S3_PREFIX, BEACON_S3_STORAGE_CLASS
  AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN
  BEACON_PKG, BEACON_UPDATE_MANIFEST_URL
  FLEET_AUTOMATIC_INSTALL=true|false   default false (pilot first)
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
    --skip-queries) SKIP_QUERIES=1 ;;
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
  cat "$body"
}

fleet_json() {
  local method="$1"
  local path="$2"
  shift 2
  fleet_request "$method" "$path" -H "Content-Type: application/json" "$@"
}

require_ok() {
  local action="$1"
  case "$FLEET_HTTP_STATUS" in
    2*) return 0 ;;
  esac
  echo "$action failed (HTTP $FLEET_HTTP_STATUS)" >&2
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
  case "$FLEET_HTTP_STATUS" in
    402|403)
      echo "Custom software packages require Fleet Premium, a specific team (not All teams), and a token that can manage that team." >&2
      ;;
  esac
  exit 1
}

echo
echo "Beacon Fleet + S3 setup"
echo "======================="
echo "This helper uploads the latest Apple Silicon Beacon .pkg to one Fleet"
echo "team, stores S3 credentials as Fleet secret variables, and creates host"
echo "scripts that enable self-updates and Vector forwarding after install."
echo

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
  echo "Checking Fleet credentials..."
  me="$(fleet_json GET /api/latest/fleet/me)"
  require_ok "Fleet authentication"
  me_name="$(printf '%s' "$me" | json_get user name)"
  me_email="$(printf '%s' "$me" | json_get user email)"
  echo "Authenticated as ${me_name:-unknown}${me_email:+ <$me_email>}"
else
  FLEET_TOKEN="${FLEET_TOKEN:-dry-run}"
fi

if [ -z "${FLEET_TEAM_ID:-}" ]; then
  if [ "$DRY_RUN" -eq 1 ]; then
    FLEET_TEAM_ID="${FLEET_TEAM_ID:-1}"
  elif [ "$ASSUME_YES" -eq 1 ]; then
    echo "FLEET_TEAM_ID is required. Custom packages cannot be added to All teams." >&2
    exit 1
  else
    echo
    echo "Available Fleet teams:"
    teams_json="$(fleet_json GET /api/latest/fleet/teams)"
    require_ok "Listing Fleet teams"
    python3 -c '
import json,sys
data=json.loads(sys.stdin.read() or "{}")
teams=data.get("teams") or []
if not teams:
    print("  (none returned; you still need a team ID, not All teams)")
    raise SystemExit
for t in teams:
    print("  {id:>6}  {name}".format(id=t.get("id"), name=t.get("name")))
' <<<"$teams_json"
    prompt_value FLEET_TEAM_ID "Fleet team ID to deploy into"
  fi
fi
if ! printf '%s' "$FLEET_TEAM_ID" | grep -Eq '^[0-9]+$'; then
  echo "FLEET_TEAM_ID must be a number. Fleet cannot add custom packages to All teams." >&2
  exit 1
fi

if [ -z "${FLEET_AUTOMATIC_INSTALL:-}" ]; then
  if yes_no "Create a Fleet automatic-install policy for this package? Start with a pilot team" n; then
    FLEET_AUTOMATIC_INSTALL="true"
  else
    FLEET_AUTOMATIC_INSTALL="false"
  fi
fi

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
CREDENTIAL_MODE="${BEACON_AWS_CREDENTIAL_MODE:-}"
if [ -z "$CREDENTIAL_MODE" ]; then
  if [ -n "${AWS_ACCESS_KEY_ID:-}" ] && [ -n "${AWS_SECRET_ACCESS_KEY:-}" ]; then
    CREDENTIAL_MODE="keys"
  elif [ "$ASSUME_YES" -eq 1 ]; then
    CREDENTIAL_MODE="none"
  else
    printf 'AWS credentials: [k]ey pair (recommended) / [n]one, I will deliver them another way [k]: ' >&2
    IFS= read -r CREDENTIAL_MODE
    case "${CREDENTIAL_MODE:-k}" in
      n|N|none) CREDENTIAL_MODE="none" ;;
      *) CREDENTIAL_MODE="keys" ;;
    esac
  fi
fi

if [ "$CREDENTIAL_MODE" = "keys" ]; then
  prompt_value AWS_ACCESS_KEY_ID "AWS access key ID"
  prompt_secret_confirm AWS_SECRET_ACCESS_KEY "AWS secret access key"
  if [ -z "${AWS_SESSION_TOKEN:-}" ] && [ "$ASSUME_YES" -eq 0 ]; then
    printf 'AWS session token (optional, Enter to skip): ' >&2
    IFS= read -r -s AWS_SESSION_TOKEN || true
    printf '\n' >&2
  fi
  if [ -z "$AWS_ACCESS_KEY_ID" ] || [ -z "$AWS_SECRET_ACCESS_KEY" ]; then
    echo "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required for key-pair mode" >&2
    exit 1
  fi
fi

PKG_PATH=""
PKG_VERSION=""
PKG_SHA=""
if [ "$SKIP_SOFTWARE" -eq 0 ]; then
  if [ "$SKIP_DOWNLOAD" -eq 1 ]; then
    PKG_PATH="${LOCAL_PKG:-}"
    if [ -z "$PKG_PATH" ] || [ ! -f "$PKG_PATH" ]; then
      echo " --pkg / BEACON_PKG must point at an existing .pkg" >&2
      exit 1
    fi
    PKG_VERSION="$(basename "$PKG_PATH")"
  else
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
echo "  Team ID:            $FLEET_TEAM_ID"
echo "  Package:            ${PKG_PATH:-skipped}"
echo "  Version:            ${PKG_VERSION:-unknown}"
echo "  Automatic install:  $FLEET_AUTOMATIC_INSTALL"
echo "  Self-update mode:   $UPDATE_MODE"
echo "  S3 bucket:          $BEACON_S3_BUCKET"
echo "  S3 prefix:          $BEACON_S3_PREFIX/{runtime,inventory}/date=YYYY-MM-DD/"
echo "  AWS region:         $AWS_REGION"
echo "  AWS credentials:    $CREDENTIAL_MODE"
echo "  Host scripts:       $ENABLE_UPDATES_SCRIPT_NAME, $CONFIGURE_S3_SCRIPT_NAME, $VALIDATE_SCRIPT_NAME"
if [ "$DRY_RUN" -eq 1 ]; then
  echo
  echo "Dry run: not calling Fleet. Generated host scripts are in:"
  echo "  $WORK_DIR"
  echo
  echo "----- $CONFIGURE_S3_SCRIPT_NAME -----"
  # Show the S3 script with Fleet secret placeholders, not live key material.
  cat "$S3_SCRIPT"
  trap - EXIT
  exit 0
fi

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
  if [ "$FLEET_HTTP_STATUS" = "404" ]; then
    echo "spec/secret_variables not found; trying custom_variables..."
    while IFS= read -r secret_item; do
      printf '%s\n' "$secret_item" >"$WORK_DIR/one-secret.json"
      fleet_json POST /api/latest/fleet/custom_variables --data @"$WORK_DIR/one-secret.json" >/dev/null
      require_ok "Creating Fleet custom variable"
    done < <(python3 -c '
import json, pathlib, sys
src = json.loads(pathlib.Path(sys.argv[1]).read_text())
prefix = "FLEET_SECRET_"
for item in src["secrets"]:
    name = item["name"]
    if name.startswith(prefix):
        name = name[len(prefix):]
    print(json.dumps({"name": name, "value": item["value"]}))
' "$WORK_DIR/secrets.json")
  else
    require_ok "Storing Fleet secret variables"
  fi
  rm -f "$WORK_DIR/secrets.json"
  echo "Fleet secret variables stored (values are hidden in the Fleet UI)."
fi

upload_or_replace_script() {
  local name="$1"
  local file="$2"
  local scripts_json existing_id
  scripts_json="$(fleet_json GET "/api/latest/fleet/scripts?team_id=${FLEET_TEAM_ID}")"
  require_ok "Listing Fleet scripts"
  existing_id="$(
    python3 -c '
import json,sys
name=sys.argv[1]
data=json.loads(sys.stdin.read() or "{}")
scripts=data.get("scripts") or []
for s in scripts:
    if s.get("name")==name:
        print(s.get("id") or "")
        break
' "$name" <<<"$scripts_json"
  )"
  if [ -n "$existing_id" ]; then
    echo "Updating Fleet script $name (id $existing_id)..."
    fleet_request PATCH "/api/latest/fleet/scripts/${existing_id}" \
      -F "script=@${file};filename=${name}" >/dev/null
    require_ok "Updating $name"
  else
    echo "Creating Fleet script $name..."
    fleet_request POST /api/latest/fleet/scripts \
      -F "team_id=${FLEET_TEAM_ID}" \
      -F "script=@${file};filename=${name}" >/dev/null
    require_ok "Creating $name"
  fi
}

if [ "$SKIP_SCRIPTS" -eq 0 ]; then
  echo
  upload_or_replace_script "$ENABLE_UPDATES_SCRIPT_NAME" "$ENABLE_UPDATES_SCRIPT"
  upload_or_replace_script "$CONFIGURE_S3_SCRIPT_NAME" "$S3_SCRIPT"
  upload_or_replace_script "$VALIDATE_SCRIPT_NAME" "$VALIDATE_SCRIPT"
fi

if [ "$SKIP_SOFTWARE" -eq 0 ]; then
  echo
  echo "Looking for an existing Beacon software title on this team..."
  titles_json="$(fleet_json GET "/api/latest/fleet/software/titles?team_id=${FLEET_TEAM_ID}&available_for_install=true&query=${SOFTWARE_QUERY}")"
  require_ok "Listing software titles"
  title_id="$(
    python3 -c '
import json,sys
data=json.loads(sys.stdin.read() or "{}")
titles=data.get("software_titles") or []
needles=("beacon", "asymptote")
for t in titles:
    blob=" ".join(str(t.get(k) or "") for k in ("name","display_name")).lower()
    if any(n in blob for n in needles):
        print(t.get("id") or "")
        break
' <<<"$titles_json"
  )"

  form=(
    -F "team_id=${FLEET_TEAM_ID}"
    -F "software=@${PKG_PATH}"
    -F "install_script=<${INSTALL_SCRIPT}"
    -F "uninstall_script=<${UNINSTALL_SCRIPT}"
    -F "self_service=false"
    -F "automatic_install=${FLEET_AUTOMATIC_INSTALL}"
  )

  if [ -n "$title_id" ]; then
    echo "Replacing package on software title $title_id..."
    fleet_request PATCH "/api/latest/fleet/software/titles/${title_id}/package" "${form[@]}" >/dev/null
    require_ok "Updating Beacon software package"
  else
    echo "Uploading ${PKG_PATH##*/} as a custom package (this can take several minutes)..."
    echo "If you self-host Fleet, the server stores packages in S3 and load-balancer timeouts should be at least 5 minutes."
    fleet_request POST /api/latest/fleet/software/package "${form[@]}" >/dev/null
    require_ok "Uploading Beacon software package"
    title_id="$(json_get software_package title_id <"$WORK_DIR/http-body")"
  fi
  echo "Beacon software title id: ${title_id:-uploaded}"
fi

create_query() {
  local name="$1"
  local description="$2"
  local sql="$3"
  local existing
  local queries_json
  queries_json="$(fleet_json GET "/api/latest/fleet/queries?team_id=${FLEET_TEAM_ID}")"
  if [ "$FLEET_HTTP_STATUS" != "200" ]; then
    echo "Skipping query '$name' (could not list queries; HTTP $FLEET_HTTP_STATUS)"
    return 0
  fi
  existing="$(
    python3 -c '
import json,sys
name=sys.argv[1]
data=json.loads(sys.stdin.read() or "{}")
for q in data.get("queries") or []:
    if q.get("name")==name:
        print(q.get("id") or "")
        break
' "$name" <<<"$queries_json"
  )"
  NAME="$name" DESC="$description" SQL="$sql" TEAM="$FLEET_TEAM_ID" \
    python3 -c '
import json,os,sys
json.dump({
  "name": os.environ["NAME"],
  "description": os.environ["DESC"],
  "query": os.environ["SQL"],
  "platform": "darwin",
  "observer_can_run": True,
  "team_id": int(os.environ["TEAM"]),
}, sys.stdout)
' >"$WORK_DIR/query.json"
  if [ -n "$existing" ]; then
    echo "Updating query $name (id $existing)..."
    fleet_json PATCH "/api/latest/fleet/queries/${existing}" --data @"$WORK_DIR/query.json" >/dev/null
    require_ok "Updating query $name"
  else
    echo "Creating query $name..."
    fleet_json POST /api/latest/fleet/queries --data @"$WORK_DIR/query.json" >/dev/null
    require_ok "Creating query $name"
  fi
}

if [ "$SKIP_QUERIES" -eq 0 ]; then
  echo
  create_query "Beacon install state" \
    "Whether /opt/beacon/bin/beacon is present." \
    "SELECT CASE WHEN COUNT(*) = 0 THEN 'not_installed' ELSE 'installed' END AS beacon_install_state FROM file WHERE path = '/opt/beacon/bin/beacon';"
  create_query "Beacon collector health" \
    "launchd state for the Beacon endpoint collector." \
    "SELECT CASE WHEN COUNT(*) = 0 THEN 'not_loaded' WHEN SUM(CASE WHEN pid > 0 THEN 1 ELSE 0 END) > 0 THEN 'running' ELSE 'loaded_not_running' END AS collector_service_health FROM launchd WHERE label = 'com.beacon.endpoint.collector';"
  create_query "Beacon S3 forwarder health" \
    "launchd state for the Vector S3 forwarder." \
    "SELECT CASE WHEN COUNT(*) = 0 THEN 'not_loaded' WHEN SUM(CASE WHEN pid > 0 THEN 1 ELSE 0 END) > 0 THEN 'running' ELSE 'loaded_not_running' END AS s3_vector_forwarder_health FROM launchd WHERE label = 'com.beacon.endpoint.s3-forwarder';"
  create_query "Beacon S3 forwarding configured" \
    "Whether the Vector S3 environment file exists." \
    "SELECT CASE WHEN COUNT(*) = 0 THEN 'not_configured' ELSE 'configured' END AS s3_vector_forwarding_state FROM file WHERE path = '/Library/Application Support/Beacon/Forwarders/s3-vector.env';"
fi

cat <<EOF

Done. Next steps in Fleet
-------------------------
1. Scope the Beacon software package to a pilot label of Apple Silicon Macs.
   Custom packages cannot target "All teams".
2. Confirm fleetd was deployed with scripts enabled (default when using Fleet
   MDM; otherwise --enable-scripts).
3. Install Beacon from Host details > Software, or wait for automatic install
   if you enabled it.
4. After the package is installed, run these scripts on the same hosts, in order:
     $ENABLE_UPDATES_SCRIPT_NAME
     $CONFIGURE_S3_SCRIPT_NAME
     $VALIDATE_SCRIPT_NAME
   Do not attach S3 setup as the package post-install script: Fleet uninstalls
   the package if post-install fails.
5. On a Mac, confirm:
     sudo launchctl print system/com.beacon.endpoint.collector
     sudo launchctl print system/com.beacon.endpoint.updater
     sudo launchctl print system/com.beacon.endpoint.s3-forwarder
6. S3 objects appear under:
     s3://$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/runtime/date=YYYY-MM-DD/
     s3://$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/inventory/date=YYYY-MM-DD/
   Vector batches for up to 5 minutes before the first upload.

IAM for the writer: s3:PutObject on arn:aws:s3:::$BEACON_S3_BUCKET/$BEACON_S3_PREFIX/*
If you use GitOps, re-apply this helper after the next GitOps run or fold the
package, scripts, and \$FLEET_SECRET_* variables into your GitOps repo.
EOF
