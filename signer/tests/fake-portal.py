#!/usr/bin/env python3
"""
Stand-in for signtools-portal in tests: same commands and JSON protocol, no Apple servers.

- login: password must be "secret"; asks for 2FA (device, offering SMS to phone id 1), code "123456"
- the Apple ID is in two teams: an individual one and an organization (FAKE_TEAMS to override)
- cert: a self-signed "Apple Development" certificate per team (OU = team id), reused afterwards
- provision: bundle ids listed in FAKE_TAKEN_IDS are "registered by someone else" (Apple result code
  9401); otherwise returns a CMS-signed development profile for the team that grants exactly the
  requested capabilities it knows (increased-memory-limit, extended-virtual-addressing)
- the saved session counts as expired while <data>/expired exists (logging in removes it), and cert
  reports team_missing for a team that isn't in the list, like signtools-portal
- every call is appended to <data>/calls.log
"""
import json
import os
import plistlib
import subprocess
import sys
import uuid
from datetime import datetime, timedelta, timezone
from pathlib import Path

DATA = Path(os.environ.get("PORTAL_DATA", "portal-data"))
TEAMS = json.loads(os.environ.get("FAKE_TEAMS", json.dumps([
    {"id": "PERSON1234", "name": "Jane Doe", "type": "Individual", "status": "active"},
    {"id": "FGHIJ67890", "name": "Example Org", "type": "Company/Organization", "status": "active"},
])))
TAKEN = set(filter(None, os.environ.get("FAKE_TAKEN_IDS", "").split(",")))
GRANTABLE = ["com.apple.developer.kernel.increased-memory-limit", "com.apple.developer.kernel.extended-virtual-addressing"]


def emit(obj):
    print(json.dumps(obj), flush=True)


def fail(msg, code=None, session_expired=False, team_missing=False):
    emit({"ok": False, "event": "error", "error": msg, "code": code,
          "session_expired": session_expired, "team_missing": team_missing})
    sys.exit(1)


def log(*parts):
    DATA.mkdir(parents=True, exist_ok=True)
    with open(DATA / "calls.log", "a") as f:
        f.write(" ".join(parts) + "\n")


def accounts():
    p = DATA / "accounts.json"
    return json.loads(p.read_text()) if p.exists() else {}


def save_accounts(a):
    DATA.mkdir(parents=True, exist_ok=True)
    (DATA / "accounts.json").write_text(json.dumps(a))


def arg(name):
    args = sys.argv[2:]
    return args[args.index(name) + 1] if name in args else None


def need_account():
    email = arg("--email")
    if email not in accounts():
        fail(f"Apple ID session expired or missing, log in again (no saved session for {email})", session_expired=True)
    if (DATA / "expired").exists():
        fail("Apple ID session expired or missing, log in again (Developer API error 1100: Your session has expired)",
             1100, session_expired=True)
    return email


def team_cert(team):
    d = DATA / "keys" / team
    d.mkdir(parents=True, exist_ok=True)
    key, cert = d / "key.pem", d / "cert.pem"
    new = not cert.exists()
    if new:
        cnf = d / "cert.cnf"
        cnf.write_text(
            "[req]\ndistinguished_name=dn\nprompt=no\n[dn]\n"
            f"CN=Apple Development: Jane Doe ({team})\nOU={team}\nO=Example\nC=US\n"
            "[ext]\nbasicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature\n"
            "extendedKeyUsage=critical,codeSigning\n"
        )
        subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(key),
                        "-out", str(cert), "-days", "365", "-config", str(cnf), "-extensions", "ext"],
                       check=True, capture_output=True)
    return key, cert, new


def main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else ""
    log(*sys.argv[1:])
    if cmd == "login":
        creds = json.loads(sys.stdin.readline())
        if creds.get("password") != "secret":
            fail("Authentication SRP error -20101: Your Apple ID or password was incorrect.")
        method = "device"
        while True:
            emit({"event": "2fa", "method": method, "phones": [{"id": 1, "last_two_digits": "42"}]})
            reply = json.loads(sys.stdin.readline())
            if reply.get("sms") is not None:
                method = "sms"
                continue
            if reply.get("code") != "123456":
                fail("Incorrect verification code")
            break
        a = accounts()
        a[creds["email"]] = {"first_name": "Jane"}
        save_accounts(a)
        (DATA / "expired").unlink(missing_ok=True)
        emit({"event": "done", "ok": True, "email": creds["email"], "teams": TEAMS})
    elif cmd == "accounts":
        emit({"ok": True, "accounts": [{"email": e, "first_name": v["first_name"]} for e, v in accounts().items()]})
    elif cmd == "logout":
        a = accounts()
        a.pop(arg("--email"), None)
        save_accounts(a)
        emit({"ok": True})
    elif cmd == "teams":
        need_account()
        emit({"ok": True, "teams": TEAMS})
    elif cmd == "cert":
        need_account()
        team = arg("--team")
        if team not in [t["id"] for t in TEAMS]:
            fail(f"team {team} is no longer available for this Apple ID", team_missing=True)
        key, cert, new = team_cert(team)
        Path(arg("--out")).write_text(key.read_text() + cert.read_text())
        emit({"ok": True, "new": new, "serial": "01"})
    elif cmd == "provision":
        need_account()
        team, bundle_id = arg("--team"), arg("--bundle-id")
        if bundle_id in TAKEN:
            fail(f"Developer API error 9401 (HTTP None): An App ID with Identifier '{bundle_id}' is not available. "
                 "Please enter a different string.", 9401)
        requested = {}
        if arg("--entitlements"):
            with open(arg("--entitlements"), "rb") as f:
                requested = plistlib.load(f)
        ents = {
            "application-identifier": f"{team}.{bundle_id}",
            "com.apple.developer.team-identifier": team,
            "get-task-allow": True,
            "keychain-access-groups": [f"{team}.*"],
        }
        for k in GRANTABLE:
            if requested.get(k):
                ents[k] = True
        key, cert, _ = team_cert(team)
        now = datetime.now(timezone.utc)
        profile = {
            "AppIDName": f"ST {bundle_id}", "ApplicationIdentifierPrefix": [team], "Platform": ["iOS"],
            "CreationDate": now, "ExpirationDate": now + timedelta(days=365), "Entitlements": ents,
            "Name": f"iOS Team Provisioning Profile: {bundle_id}", "ProvisionedDevices": ["00008110-000A1B2C3D4E5F60"],
            "TeamIdentifier": [team], "TeamName": "Example", "UUID": str(uuid.uuid4()), "Version": 1,
        }
        raw = Path(arg("--out")).with_suffix(".plist")
        raw.write_bytes(plistlib.dumps(profile))
        subprocess.run(["openssl", "cms", "-sign", "-nodetach", "-binary", "-outform", "DER", "-in", str(raw),
                        "-signer", str(cert), "-inkey", str(key), "-out", arg("--out")], check=True, capture_output=True)
        log("granted", bundle_id, ",".join(k for k in GRANTABLE if k in ents) or "-")
        emit({"ok": True, "app_id_id": "X" + bundle_id, "identifier": bundle_id, "warnings": []})
    else:
        fail(f"unknown command {cmd}")


main()
