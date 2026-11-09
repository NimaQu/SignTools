#!/usr/bin/env bash
# End-to-end test of signing inside SignTools (builder.local) with the Linux signer.
#
#   SERVER_BIN=path/to/SignTools signer/tests/e2e.sh
#
# MODE=appleid (default): logs in through the web UI with the fake portal (password, SMS 2FA), adds the
#   organization team of the Apple ID as a signing profile, and signs an app whose bundle id is "taken"
#   (must fall back to <id>.<team id>) and which requests increased-memory-limit (must be granted).
# MODE=cert: a classic profile (cert.p12 + prov.mobileprovision).
# Either way the app is picked from import_dir and never goes over HTTP.
#   BIG_MB=300   add a random file of this size to the app
#   PORTAL_BIN   use a real signtools-portal instead of the fake (only for manual tests!)
# Needs: curl, python3 (cryptography, lief), unzip, zip, openssl, rcodesign, clang + lld.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
E=${E2E_DIR:-/tmp/signtools-e2e}
MODE=${MODE:-appleid}
: "${SERVER_BIN:?set SERVER_BIN to a SignTools binary built from this repository}"
rm -rf "$E"; mkdir -p "$E"/{data/profiles,data/import}; cd "$E"
cp "$SERVER_BIN" SignTools && chmod +x SignTools

bash "$ROOT/tests/make-fixtures.sh" "$E/fx" >/dev/null
if [[ -n "${BIG_MB:-}" ]]; then
  mkdir big && (cd big && unzip -qq ../fx/unsigned.ipa && head -c "$((BIG_MB * 1024 * 1024))" /dev/urandom > Payload/Demo.app/romfs.img \
    && zip -qry -0 ../data/import/Demo.ipa Payload) && rm -rf big
else
  cp fx/unsigned.ipa data/import/Demo.ipa
fi

cat > signer-cfg.yml <<EOF
builder:
  local:
    enable: true
    sign_script: $ROOT/sign.py
server_url: http://unreachable.invalid:8080
save_dir: data
import_dir: $E/data/import
cleanup_interval_mins: 1
sign_timeout_mins: 15
apple_id:
  enable: true
  portal_bin: ${PORTAL_BIN:-$ROOT/tests/fake-portal.py}
basic_auth: {enable: false}
EOF

if [[ "$MODE" == cert ]]; then
  mkdir -p data/profiles/ent
  cp fx/{cert.p12,cert_pass.txt,prov.mobileprovision} data/profiles/ent/
  printf 'Enterprise (test)' > data/profiles/ent/name.txt
fi

trap 'kill $(jobs -p) 2>/dev/null || true' EXIT
FAKE_TAKEN_IDS=${FAKE_TAKEN_IDS:-com.example.demo} ./SignTools -config signer-cfg.yml -port 8080 > server.log 2>&1 &
for _ in $(seq 1 50); do curl -fs -o /dev/null http://127.0.0.1:8080/ && break; sleep 0.2; done
B=http://127.0.0.1:8080
fail() { echo "FAIL: $*"; exit 1; }

if [[ "$MODE" == appleid ]]; then
  TEAM=FGHIJ67890
  page=$(curl -fsS -X POST $B/appleid/login -d email=jane@example.com -d password=wrong)
  grep -q "Login failed" <<<"$page" || fail "wrong password not reported"
  page=$(curl -fsS -X POST $B/appleid/login -d email=jane@example.com -d password=secret)
  login=$(grep -o 'action="/appleid/login/[0-9a-f-]*"' <<<"$page" | head -1 | cut -d/ -f4 | tr -d '"')
  [[ -n "$login" ]] || fail "no 2FA step: $page"
  page=$(curl -fsS -X POST "$B/appleid/login/$login" -d sms=1)
  grep -q "text message" <<<"$page" || fail "SMS 2FA not shown"
  code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$B/appleid/login/$login" -d code=123456)
  [[ "$code" == 302 ]] || fail "2FA code not accepted ($code)"
  page=$(curl -fsS $B/appleid)
  grep -q 'data-team="PERSON1234"' <<<"$page" && grep -q "data-team=\"$TEAM\"" <<<"$page" || fail "teams not listed"
  echo "login + SMS 2FA ok, both teams listed"
  curl -fsS -o /dev/null -X POST $B/appleid/profiles -d email=jane@example.com -d team_id=$TEAM
  profile=$(ls data/profiles | grep -i "$TEAM")
  index=$(curl -fsS $B/)
  grep -q "value=\"$profile\" account=\"false\" appleid=\"true\"" <<<"$index" || fail "profile not offered in upload form"
  EXPECT_SIGNER="Apple Development: Jane Doe ($TEAM)"
  EXPECT_MAIN=com.example.demo.$TEAM
else
  TEAM=ABCDE12345; profile=ent; EXPECT_SIGNER="iPhone Distribution: Test Corp"; EXPECT_MAIN=com.example.demo
fi

curl -fsS -o /dev/null -X POST $B/apps -F file_local=Demo.ipa -F profile_id="$profile" -F builder_id=Local \
  -F id=id_original -F bundle_name="Demo Signed"
app=""
for _ in $(seq 1 150); do
  app=$(ls data/apps 2>/dev/null | head -1 || true)
  [[ -n "$app" && -s "data/apps/$app/signed" ]] && break
  sleep 1
done
logs=$(curl -fsS $B/local-builder)
[[ -n "$app" && -s "data/apps/$app/signed" ]] || { echo "$logs" | tail -60; fail "signing did not finish"; }
grep -q ">done<" <<<"$logs" || fail "log page does not show the job as done"

plain=$(sed 's/\x1b\[[0-9;]*m//g' server.log)
echo "HTTP downloads of the unsigned app: $(grep -c "uri=/jobs/[^ ]*/unsigned" <<<"$plain" || true)"
echo "tus uploads of the signed app:      $(grep -c "uri=/jobs/[^ ]*/tus/" <<<"$plain" || true)"
[[ $(stat -c %i data/import/Demo.ipa) == $(stat -c %i "data/apps/$app/unsigned") ]] || fail "import was copied, not linked"
[[ $(cat "data/apps/$app/bundle_id") == "$EXPECT_MAIN" ]] || fail "bundle id $(cat "data/apps/$app/bundle_id"), want $EXPECT_MAIN"
echo "bundle id: $EXPECT_MAIN"
if [[ "$MODE" == appleid ]]; then
  grep -q "provision .*--bundle-id com.example.demo " data/portal/calls.log || fail "original id not tried first"
  grep -q "granted com.example.demo.$TEAM com.apple.developer.kernel.increased-memory-limit" data/portal/calls.log \
    || fail "increased-memory-limit not requested for the main app"
  grep -q "granted com.example.demo.$TEAM.share" data/portal/calls.log || fail "app extension not provisioned"
  echo "App IDs: original taken -> fell back to <id>.$TEAM; extension provisioned; increased-memory-limit granted"
fi

curl -fsS -o signed.ipa "$B/apps/$app/signed"
EXPECT_SIGNER="$EXPECT_SIGNER" python3 "$ROOT/tests/verify-ipa.py" signed.ipa "$TEAM" memory
