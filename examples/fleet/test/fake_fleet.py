#!/usr/bin/env python3
"""A small stateful stand-in for the Fleet REST API, for testing
configure-beacon-macos-s3.sh without a Fleet server.

It implements only the endpoints the helper calls, keeps state across requests
so a second helper run sees what the first one created, and appends one JSON
line per request to --log so a test can assert on exactly what was sent.

Scenario files (--fixture) seed the state and can force responses:

  {
    "version": "4.92.0",
    "me": {"user": {...}},
    "teams": [...],
    "titles": [{"id": 42, "name": "endpoint", "bundle_identifier": "...",
                "software_package": {...}, "packages": [...]}],
    "title_page_size": 1,          # force small title pages to exercise paging
    "policies": [...],
    "scripts": [...],
    "queries": [...],
    "conflict_on_upload": false,   # answer POST /software/package with 409
    "overrides": {"PUT /api/latest/fleet/spec/secret_variables": [403, {"message": "forbidden"}]}
  }

Like Fleet 4.82+, a request that names both team_id and fleet_id gets a 400.
"""

import argparse
import email.parser
import email.policy
import hashlib
import json
import os
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

PKG_ID = "ai.asymptote.beacon.endpoint"


class State:
    def __init__(self, fixture, pkg_path):
        self.lock = threading.Lock()
        self.fixture = fixture
        self.version = fixture.get("version", "4.92.0")
        self.me = fixture.get("me") or {
            "user": {"name": "Admin", "email": "admin@example.com", "global_role": "admin", "teams": []}
        }
        self.teams = fixture.get("teams") or [{"id": 7, "name": "Pilot"}]
        self.titles = fixture.get("titles") or []
        self.title_page_size = fixture.get("title_page_size")
        self.policies = fixture.get("policies") or []
        self.scripts = fixture.get("scripts") or []
        self.queries = fixture.get("queries") or []
        self.overrides = fixture.get("overrides") or {}
        self.conflict_on_upload = bool(fixture.get("conflict_on_upload"))
        self.secrets = {}
        self.next_id = 1000
        self.pkg_path = pkg_path

    def new_id(self):
        self.next_id += 1
        return self.next_id


def parse_multipart(content_type, body):
    raw = b"Content-Type: " + content_type.encode() + b"\r\n\r\n" + body
    msg = email.parser.BytesParser(policy=email.policy.HTTP).parsebytes(raw)
    fields = {}
    for part in msg.iter_parts():
        name = part.get_param("name", header="content-disposition")
        filename = part.get_param("filename", header="content-disposition")
        data = part.get_payload(decode=True) or b""
        if filename is not None:
            entry = {"filename": filename, "sha256": hashlib.sha256(data).hexdigest(), "size": len(data)}
            if len(data) < 65536:
                try:
                    entry["content"] = data.decode("utf-8")
                except UnicodeDecodeError:
                    pass
            fields[name] = entry
        else:
            fields[name] = data.decode("utf-8")
    return fields


def page(items, query, forced_size=None):
    p = int((query.get("page") or ["0"])[0])
    size = forced_size or int((query.get("per_page") or ["100"])[0])
    chunk = items[p * size:(p + 1) * size]
    return chunk, {"has_next_results": (p + 1) * size < len(items), "has_previous_results": p > 0}


def title_summary(t):
    out = {k: v for k, v in t.items() if k not in ("detail_packages",)}
    return out


class Handler(BaseHTTPRequestHandler):
    server_version = "fake-fleet"

    def log_message(self, *args):
        pass

    def reply(self, status, obj=None, raw=None, content_type="application/json"):
        body = raw if raw is not None else json.dumps(obj if obj is not None else {}).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def handle_any(self, method):
        st = self.server.state
        url = urlparse(self.path)
        path = url.path
        query = parse_qs(url.query)
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length) if length else b""
        ctype = self.headers.get("Content-Type") or ""
        record = {"method": method, "path": path, "query": query}
        form = None
        js = None
        if ctype.startswith("multipart/form-data"):
            form = parse_multipart(ctype, body)
            record["form"] = form
        elif body:
            try:
                js = json.loads(body)
            except ValueError:
                js = None
            record["json"] = js
        with open(self.server.log_path, "a") as fh:
            fh.write(json.dumps(record) + "\n")

        names = set(query) | set(form or {}) | (set(js) if isinstance(js, dict) else set())
        if "team_id" in names and "fleet_id" in names:
            return self.reply(400, {"message": "Specify only one of team_id or fleet_id"})

        key = "%s %s" % (method, path)
        if key in st.overrides:
            status, obj = st.overrides[key]
            return self.reply(status, obj)

        with st.lock:
            return self.route(st, method, path, query, form, js)

    def route(self, st, method, path, query, form, js):
        if method == "GET" and path == "/manifest.json":
            data = open(st.pkg_path, "rb").read()
            name = os.path.basename(st.pkg_path)
            version = re.sub(r"^BeaconEndpointAgent-(.*)-arm64\.pkg$", r"\1", name)
            base = "http://%s:%d" % self.server.server_address[:2]
            return self.reply(200, {
                "version": version,
                "artifacts": {"darwin_arm64": {"url": "%s/%s" % (base, name), "sha256": hashlib.sha256(data).hexdigest()}},
            })
        if method == "GET" and path == "/" + os.path.basename(st.pkg_path):
            return self.reply(200, raw=open(st.pkg_path, "rb").read(), content_type="application/octet-stream")

        if not path.startswith("/api/latest/fleet/"):
            return self.reply(404, {"message": "not found"})
        rest = path[len("/api/latest/fleet/"):]

        if method == "GET" and rest == "version":
            return self.reply(200, {"version": st.version})
        if method == "GET" and rest == "me":
            return self.reply(200, st.me)
        if method == "GET" and rest == "teams":
            items, _ = page(st.teams, query)
            return self.reply(200, {"teams": items})

        if method == "PUT" and rest == "spec/secret_variables":
            for s in (js or {}).get("secrets") or []:
                st.secrets[s["name"]] = s["value"]
            return self.reply(200, {})

        if method == "GET" and rest == "scripts":
            items, meta = page(st.scripts, query)
            return self.reply(200, {"scripts": items, "meta": meta})
        if method == "POST" and rest == "scripts":
            script = form["script"]
            sid = st.new_id()
            st.scripts.append({"id": sid, "name": script["filename"], "content": script.get("content", "")})
            return self.reply(200, {"script_id": sid})
        m = re.fullmatch(r"scripts/(\d+)", rest)
        if method == "PATCH" and m:
            for s in st.scripts:
                if s["id"] == int(m.group(1)):
                    s["content"] = form["script"].get("content", "")
                    return self.reply(200, {"script_id": s["id"]})
            return self.reply(404, {"message": "script not found"})

        if method == "GET" and rest == "software/titles":
            items, meta = page(st.titles, query, st.title_page_size)
            return self.reply(200, {"software_titles": [title_summary(t) for t in items], "count": len(st.titles), "meta": meta})
        m = re.fullmatch(r"software/titles/(\d+)", rest)
        if method == "GET" and m:
            for t in st.titles:
                if t["id"] == int(m.group(1)):
                    detail = dict(t)
                    detail["packages"] = t.get("detail_packages") or t.get("packages") or []
                    return self.reply(200, {"software_title": detail})
            return self.reply(404, {"message": "title not found"})
        if method == "POST" and rest == "software/package":
            if st.conflict_on_upload:
                return self.reply(409, {"message": "already has an installer available"})
            software = form["software"]
            tid = st.new_id()
            pkg = {
                "name": software["filename"],
                "hash_sha256": software["sha256"],
                "install_script": form.get("install_script", ""),
                "uninstall_script": form.get("uninstall_script", ""),
                "pre_install_query": form.get("pre_install_query", ""),
                "display_name": "",
                "self_service": form.get("self_service") == "true",
                "title_id": tid,
            }
            st.titles.append({"id": tid, "name": "endpoint", "bundle_identifier": PKG_ID, "software_package": pkg})
            return self.reply(200, {"software_package": {"title_id": tid, "installer_id": st.new_id()}})
        m = re.fullmatch(r"software/titles/(\d+)/package", rest)
        if method == "PATCH" and m:
            for t in st.titles:
                if t["id"] == int(m.group(1)):
                    if len(t.get("detail_packages") or []) > 1 and "installer_id" not in form:
                        return self.reply(400, {"message": "installer_id is required when the title has multiple packages"})
                    pkg = t.setdefault("software_package", {})
                    for k in ("install_script", "uninstall_script", "pre_install_query", "display_name"):
                        if k in form:
                            pkg[k] = form[k]
                    if "self_service" in form:
                        pkg["self_service"] = form["self_service"] == "true"
                    if "software" in form:
                        pkg["name"] = form["software"]["filename"]
                        pkg["hash_sha256"] = form["software"]["sha256"]
                    return self.reply(200, {"software_package": pkg})
            return self.reply(404, {"message": "title not found"})

        m = re.fullmatch(r"teams/(\d+)/policies", rest)
        if method == "GET" and m:
            items, _ = page(st.policies, query)
            return self.reply(200, {"policies": items})
        if method == "POST" and m:
            pol = {k: v for k, v in (js or {}).items() if k != "software_title_id"}
            pol["id"] = st.new_id()
            if (js or {}).get("software_title_id"):
                pol["install_software"] = {"software_title_id": js["software_title_id"], "name": "endpoint"}
            st.policies.append(pol)
            return self.reply(200, {"policy": pol})
        m = re.fullmatch(r"teams/(\d+)/policies/(\d+)", rest)
        if method == "PATCH" and m:
            for pol in st.policies:
                if pol["id"] == int(m.group(2)):
                    for k, v in (js or {}).items():
                        if k == "software_title_id":
                            if v is None:
                                pol.pop("install_software", None)
                            else:
                                pol["install_software"] = {"software_title_id": v, "name": "endpoint"}
                        else:
                            pol[k] = v
                    return self.reply(200, {"policy": pol})
            return self.reply(404, {"message": "policy not found"})
        m = re.fullmatch(r"teams/(\d+)/policies/delete", rest)
        if method == "POST" and m:
            ids = set((js or {}).get("ids") or [])
            before = len(st.policies)
            st.policies[:] = [p for p in st.policies if p["id"] not in ids]
            return self.reply(200, {"deleted": before - len(st.policies)})

        if method == "GET" and rest == "queries":
            items, meta = page(st.queries, query)
            return self.reply(200, {"queries": items, "meta": meta})
        if method == "POST" and rest == "queries":
            q = dict(js or {})
            q["id"] = st.new_id()
            st.queries.append(q)
            return self.reply(200, {"query": q})
        m = re.fullmatch(r"queries/(\d+)", rest)
        if method == "PATCH" and m:
            for q in st.queries:
                if q["id"] == int(m.group(1)):
                    q.update(js or {})
                    return self.reply(200, {"query": q})
            return self.reply(404, {"message": "query not found"})

        return self.reply(404, {"message": "not found: %s %s" % (method, path)})

    def do_GET(self):
        self.handle_any("GET")

    def do_POST(self):
        self.handle_any("POST")

    def do_PUT(self):
        self.handle_any("PUT")

    def do_PATCH(self):
        self.handle_any("PATCH")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fixture", required=True)
    ap.add_argument("--log", required=True)
    ap.add_argument("--port-file", required=True)
    ap.add_argument("--pkg", required=True)
    args = ap.parse_args()
    fixture = json.load(open(args.fixture))
    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    server.state = State(fixture, args.pkg)
    server.log_path = args.log
    open(args.log, "a").close()
    tmp = args.port_file + ".tmp"
    with open(tmp, "w") as fh:
        fh.write(str(server.server_address[1]))
    os.replace(tmp, args.port_file)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass


if __name__ == "__main__":
    sys.exit(main())
