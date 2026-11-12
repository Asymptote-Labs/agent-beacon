#!/bin/sh
# Smoke-tests a beacon-gateway image: it reports the expected version, starts on its built-in
# configuration, answers a LiteLLM guardrail call with no action (observe only), counts the call on
# /stats without a failure, records the call's prompt in gateway-events.jsonl, and is still running
# at the end. CI runs it on the image it builds, release-gateway.yml on the image it pushed, by
# digest.
#
#   sh gateway/scripts/image-smoke.sh <image> [expected-version]
#
# The gateway runs in a network namespace of its own and every request comes from a curl container
# joined to it, so 127.0.0.1 is the gateway and nothing else: no host port can collide with it or
# answer in its place.
set -eu

image=$1
want_version=${2:-}
# curl 8.23.0, multi-platform index. Bump the digest, never back to a bare tag.
curl_image=curlimages/curl@sha256:3fb7e13e5d179a845c97d78204f3f1064d1907037f26a3ebf91710571151fd93
work=$(mktemp -d)
cid=
cleanup() {
	[ -z "$cid" ] || docker rm -f "$cid" > /dev/null 2>&1 || true
	rm -rf "$work"
}
trap cleanup EXIT
fail() {
	echo "image-smoke: $*" >&2
	if [ -n "$cid" ]; then
		echo "--- server log" >&2
		docker logs "$cid" >&2 2>&1 || true
	fi
	exit 1
}
# Runs curl inside the gateway's network namespace.
gwcurl() {
	docker run --rm --network "container:$cid" "$curl_image" "$@"
}

line=$(docker run --rm "$image" -version)
echo "image-smoke: $line"
case "$line" in
"beacon-gateway "*) ;;
*) fail "-version printed '$line'" ;;
esac
got_version=$(echo "$line" | awk '{print $2}')
if [ -n "$want_version" ] && [ "$got_version" != "$want_version" ]; then
	fail "image is version $got_version, expected $want_version"
fi

cid=$(docker run -d "$image")
gwcurl -fsS --retry 20 --retry-delay 1 --retry-all-errors -o /dev/null http://127.0.0.1:4029/healthz ||
	fail "/healthz never answered"

prompt="image smoke prompt $(date +%s)"
reply=$(gwcurl -fsS -H 'Content-Type: application/json' \
	http://127.0.0.1:4019/beta/litellm_basic_guardrail_api \
	--data "{\"input_type\":\"request\",\"litellm_call_id\":\"image-smoke-1\",\"model\":\"anthropic/claude-sonnet-4-5\",\
\"structured_messages\":[{\"role\":\"system\",\"content\":\"You are a smoke test.\"},{\"role\":\"user\",\"content\":\"$prompt\"}],\
\"texts\":[\"$prompt\"],\"request_headers\":{\"session_id\":\"image-smoke\"}}") ||
	fail "the guardrail call failed"
case "$reply" in
*'"NONE"'*) ;;
*) fail "the guardrail call answered '$reply', expected action NONE" ;;
esac

# Records are written in batches off the call path: give the output a few seconds.
i=0
until docker cp "$cid:/home/nonroot/gateway-events.jsonl" "$work/events.jsonl" > /dev/null 2>&1 &&
	grep -q '"prompt.submitted"' "$work/events.jsonl" && grep -qF "$prompt" "$work/events.jsonl"; do
	i=$((i + 1))
	[ "$i" -lt 20 ] || fail "no prompt.submitted event carrying the prompt in gateway-events.jsonl"
	sleep 0.5
done

stats=$(gwcurl -fsS http://127.0.0.1:4029/stats) || fail "/stats did not answer"
case "$stats" in
*'"calls":0,'*) fail "/stats counted no call: $stats" ;;
*'"failed":0,'*) ;;
*) fail "/stats counts a failed call: $stats" ;;
esac
[ "$(docker inspect -f '{{.State.Running}}' "$cid")" = true ] || fail "the gateway exited"
echo "image-smoke: ok: answered NONE and recorded $(wc -l < "$work/events.jsonl" | tr -d ' ') events"
