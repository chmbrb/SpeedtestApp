# Running SpeedtestApp with Docker

One command brings up the whole thing: the database, the backend, the web interface, and a **private ntfy server** for phone
notifications. Everything is in `deploy/`.

```
cd deploy
./setup.sh 192.168.0.7          # your machine's address as your phone sees it; writes .env with fresh secrets
docker compose up -d --build
```

Open `https://192.168.0.7:8443`. The first time on each device you have to trust the HTTPS certificate once: see
[HTTPS on your network](#https-on-your-network). (Plain `http://192.168.0.7:8080` also works, but browsers only allow installing the app
and notifications on https pages.) `setup.sh` prints everything else you need: the ntfy login for your phone, the topic, and the token
for feeding in results. It never overwrites an existing `.env`.

You need Docker with the Compose plugin (or Podman with a Docker-compatible socket). The first start builds two small images and
takes a couple of minutes.

## What runs

| Service | What it is | Reachable from |
|---|---|---|
| `https` | Caddy: HTTPS in front of the app, using its own private certificate authority (no domain, nothing from the internet) | your network, port `HTTPS_PORT` (8443) |
| `frontend` | nginx: serves the app and forwards `/api` to the backend, including the live test stream | your network, port `HTTP_PORT` (8080), and the `https` service |
| `backend` | the Go server, running as a limited database user, read-only filesystem, no extra privileges | the stack only |
| `db` | PostgreSQL 17 | the stack only |
| `migrate` | applies the schema on every start, then exits; this is also how upgrades reach an existing database | n/a |
| `ntfy` | your own notification server, **private**: nobody can read or publish without a login | your network, port `NTFY_PORT` (8081) |

Data lives in Docker volumes (`db`, `ntfy`, `ookla`, and `caddy-data` for the HTTPS certificate authority). `docker compose down` keeps them; `docker compose down -v` deletes them.

## Phone notifications (ntfy)

1. Install the ntfy app (Google Play, F-Droid, or the iOS App Store).
2. Add a subscription. Use **your server** (`http://<host>:8081`, not ntfy.sh), the topic and the login `setup.sh` printed (user `phone`).
3. Check it: `docker compose run --rm backend notify-test`.

The stack gives each side only what it needs: the backend can **only publish** to the topic, and the phone login can **only read** it.
Anything else is refused. The thresholds that decide *when* you are notified are described in
[backend-go/README.md](../backend-go/README.md#notifications-ntfy).

**iPhones** need one extra setting for instant delivery: put `NTFY_UPSTREAM_BASE_URL=https://ntfy.sh` in `.env`. Only a "something
arrived" poll goes there, never the message. Your phone must also be able to reach `NTFY_BASE_URL`; for use outside your home network
put your own HTTPS reverse proxy in front.

## HTTPS on your network

Browsers only let a site install itself as an app, and only show it notifications, when it is served over HTTPS. That normally needs a
domain name and a certificate from the internet. This stack does neither: the `https` service runs a small **private certificate
authority** of its own, makes a certificate for your machine's address, and renews it by itself. It never contacts anything outside your
network.

The price is a one-time step per device: your phone or computer has to be told to trust *your* authority, because no one else vouches for
it. Until it does, the browser shows a certificate warning and the app can't install or send notifications (the dashboard itself still
opens, and the Notifications section explains what is missing).

**The address.** `setup.sh` puts your machine's address in `HTTPS_HOSTS` in `.env`. To also reach it by name (`192.168.0.7, speedtest.lan`),
list every address or name people type, separated by commas, change `APP_URL` to match, and run `docker compose up -d`. If your router
changes the machine's address, the certificate no longer matches: give the machine a fixed address (a DHCP reservation) first.

**Trust the authority, once per device**

1. Download the certificate from `https://<address>:8443/ca.crt`. The browser warns; the certificate is public and safe to download past
   the warning. To be certain it is yours, compare its fingerprint with the one on the server (the install dialogs show it too):
   `docker compose cp https:/data/caddy/pki/authorities/local/root.crt . && openssl x509 -in root.crt -noout -fingerprint -sha256`
2. Install it as a trusted authority:

| Device | Steps |
|---|---|
| iPhone, iPad | Open the downloaded file, then Settings → Profile Downloaded → Install. Then Settings → General → About → Certificate Trust Settings → turn the switch on. |
| Android | Settings → Security (or Security & privacy) → Encryption & credentials → Install a certificate → CA certificate → choose the file. |
| Windows | Double-click the file → Install Certificate → Trusted Root Certification Authorities. |
| macOS | Open it in Keychain Access (System keychain), double-click it, set Trust to Always Trust. Safari and Chrome use this. |
| Linux, Chrome | `certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n speedtest-local -i root.crt` |
| Firefox (any system) | Settings → Privacy & Security → Certificates → View Certificates → Authorities → Import. |

3. Reload the app. The warning is gone, and the install button and notification switch work.

**Keep in mind**

- Only trust the authority on devices you own. Whoever holds its private key could impersonate websites to every device that trusts it.
  The key lives in the `caddy-data` volume: keep that volume, and any backup of it, private.
- If you delete that volume (`docker compose down -v`), a new authority is made and each device must trust the new one (and you can
  remove the old one).
- The plain `http://…:8080` address keeps working. If you only want HTTPS, delete the `ports:` entry of the `frontend` service in
  `docker-compose.yml`; the `https` service still reaches it inside the stack.

## Install the app and browser notifications

**Install.** On Chrome, Edge and Android, the app offers an **Install** button at the top of the page (or use the browser's own install
button in the address bar). On iPhone and iPad, tap Share, then **Add to Home Screen** (the app shows the same hint). It then opens in its
own window and keeps showing the last results while you are offline.

**Notifications on a device.** In the dashboard open **Notifications → On this device → Turn on notifications**, allow the browser's
question, and press **Send a test**. You get the same alerts ntfy sends (slow downloads, packet loss, devices that stopped testing,
and recoveries), so `NTFY_TOPIC` must be set (`setup.sh` does it). You can use ntfy, browser notifications, or both.

- **iPhone and iPad** (iOS 16.4 or newer): notifications work only for the app on your Home Screen, so install it first, open it from the
  Home Screen, then turn them on.
- **Works with the browser closed**, because the notification arrives through the browser maker's push service.
- **What leaves your network.** The server sends each notification to the push service of your browser's maker (Google, Mozilla,
  Apple or Microsoft), which relays it to your device. The message is encrypted so that only your device can read it, but that company
  learns that *a message went to your browser* and when. ntfy on your own server avoids that. The server therefore needs internet access
  to send these, and refuses to send to any address that isn't one of those push services.
- Settings (all optional, in `.env`): `PUSH_ENABLED=no` turns it off; `PUSH_SUBJECT` is who the server says it is to the push services
  (an https address or email; defaults to `APP_URL`); `PUSH_ALLOWED_HOSTS` adds push-service hosts beyond the built-in list.
- A device is removed from the list when its browser says it is gone, or after five failed deliveries in a row. Turn notifications on again
  to re-register it. At most 50 devices can be registered.

## Getting results in

The dashboard shows what is in the database, so something has to run speed tests and send the results. On the machine that should be
tested, run the collector (`collector-go/`, full guide in [COLLECTOR.md](COLLECTOR.md)) with the token `setup.sh` printed:

```
SPEEDTEST_API_TOKEN=<token> speedtest-collector daemon --api-url http://<host>:8080 --connection wired --accept-ookla-license
```

It tests every two hours by default, keeps results on disk if the server is unreachable, and sends them later. The token decides the
device name (`server` by default). To add another device, add `name=token` to `API_TOKENS` in `.env` (comma separated) and run
`docker compose up -d`.

## The "Test the server" button and Ookla's terms

Ookla's command-line tool is **not** in the image, and nothing here accepts its terms for you. It is licensed for personal,
non-commercial use, and you must read and accept the terms yourself: <https://www.speedtest.net/about/eula>.

If you agree, set `OOKLA_ACCEPT_LICENSE=yes` in `.env` and run `docker compose up -d`. On the next start the backend downloads the tool
straight from Ookla, checks it against a checksum pinned in the image (a changed or tampered file is refused), and keeps it in the
`ookla` volume. Anything other than exactly `yes` does nothing. Without it the dashboard says why and offers the browser (NDT7) test
only, which has no such requirement (M-Lab publishes those results, including the IP address, and asks people to accept its data policy
first).

## Settings

All in `deploy/.env` (see `.env.example` for every setting). Common ones:

| Setting | Meaning |
|---|---|
| `HTTP_PORT`, `HTTPS_PORT`, `NTFY_PORT` | ports published on this machine |
| `HTTPS_HOSTS` | every address or name people type to reach the app over https, comma separated (the certificate covers exactly these) |
| `APP_URL`, `NTFY_BASE_URL` | how phones and browsers reach the app and ntfy (tapping a notification opens `APP_URL`) |
| `TIMEZONE` | default time zone for charts |
| `API_TOKENS` | who may send results: `device=token,device2=token2` |
| `OOKLA_ACCEPT_LICENSE` | see above |
| `DEVICE_NAME`, `SERVER_CONNECTION_TYPE` | what tests run by the server itself are recorded as |

Further tuning (alert thresholds, rate limits) uses the variables in `backend-go/README.md`; add them to the `backend` service's
`environment:` in `docker-compose.yml`.

## Upgrading

```
git pull
docker compose up -d --build
```

The `migrate` service applies any new schema before the backend starts.

## Things to know

- **Your own reverse proxy instead of the bundled HTTPS** (for example one that already has certificates for a domain): point it at port 8080
  and drop the `https` service. The bundled nginx records the address it sees on that port, so tell it about your proxy
  (`set_real_ip_from` in `frontend-react/docker/default.conf.template`), or every visitor will look like the proxy.
- **Starting a test needs no login.** It is rate-limited and only one server test runs at a time, but anyone who can reach the dashboard
  can start one. Keep the port off the internet unless you add your own access control.
- **Anyone who can open the dashboard can turn on notifications for their own device** and so will see the alerts, just as they can already
  read the results. The server only ever sends to real browser push services, never to an address the visitor chooses.
- **Backups:** `docker compose exec -T db pg_dump -U postgres speedtestdb > backup.sql`.
- **Secrets:** `.env` holds every password. It is created readable by you only; keep it out of git (it is ignored by default).
- **Moving existing data in:** see `sql/postgres/002_migrate_data.sql` and `scripts/export_speedtests.py`; load into this database with
  `docker compose exec -T db psql -U postgres speedtestdb`.
