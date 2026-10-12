# Speedtest collector

`speedtest-collector` is one small program with no dependencies. It runs speed tests on a device and sends the
results to your SpeedtestApp. Use it on the wired server and on any other device (a laptop or Wi-Fi machine) that
you want to measure. Each device has its own token, so results show up under that device's name.

## 1. Get it

Build all platforms (needs Go): `cd collector-go && ./build.sh 0.1.0`. The binaries and `SHA256SUMS` land in
`collector-go/dist/`. Check with `sha256sum -c SHA256SUMS`, then copy the right file to the device.

## 2. Create a token for the device

On the server, add a `name=token` pair to `API_TOKENS` in `deploy/.env` (the name becomes the device name), then
restart the backend. Make the token long and random, for example `openssl rand -hex 24`.

## 3. Settings

| Setting | Flag | Environment |
|---|---|---|
| API address | `--api-url` | `SPEEDTEST_API_URL` |
| Token | `--token-file` | `SPEEDTEST_API_TOKEN` (the token is never a flag) |
| How this device connects | `--connection wired\|wireless` | `SPEEDTEST_CONNECTION` |
| Tests | `--provider ookla,ndt7` | `SPEEDTEST_PROVIDER` |
| Ookla terms accepted | `--accept-ookla-license` | `OOKLA_ACCEPT_LICENSE=yes` |
| M-Lab data policy accepted | `--accept-mlab-policy` | `MLAB_ACCEPT_POLICY=yes` |
| Time between tests (daemon) | `--interval` (5m to 7d, default 2h) | `SPEEDTEST_INTERVAL` |

Ookla needs its own command-line tool (`speedtest`) installed, and you must read and accept its terms yourself.
NDT7 is built in but needs `--accept-mlab-policy`, because M-Lab publishes test data.

Check everything first: `speedtest-collector check`.

## 4. Run it

- Once: `speedtest-collector run` (for cron or a timer).
- Continuously: `speedtest-collector daemon` (tests every interval with a random spread; no cron needed).
- Send results that were waiting: `speedtest-collector flush`.

If the API is unreachable the result is kept on disk and sent on the next run. Results the server refuses are moved
to `rejected/` in the spool folder so they never block the queue.

Exit codes: 0 ok, 1 a test failed or a result was refused, 2 wrong settings, 3 another test is running,
4 the token was refused.

## Linux (systemd)

`/etc/systemd/system/speedtest-collector.service`:

```ini
[Unit]
Description=Speedtest collector
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/speedtest-collector.env
ExecStart=/usr/local/bin/speedtest-collector daemon
Restart=on-failure
DynamicUser=yes
StateDirectory=speedtest-collector
Environment=SPEEDTEST_SPOOL_DIR=/var/lib/speedtest-collector
# Ookla's tool crashes ("basic_string::_M_construct null not valid") when HOME is unset, as it is for a DynamicUser
# service, and it stores its license acceptance under HOME. The state directory is writable, so use it.
Environment=HOME=/var/lib/speedtest-collector

[Install]
WantedBy=multi-user.target
```

`/etc/speedtest-collector.env` (mode 0600):

```
SPEEDTEST_API_URL=http://192.168.0.7:8080
SPEEDTEST_API_TOKEN=...
SPEEDTEST_CONNECTION=wired
OOKLA_ACCEPT_LICENSE=yes
```

Then `systemctl enable --now speedtest-collector`.

## macOS and Windows

- macOS: run `speedtest-collector daemon` from a launchd agent (`~/Library/LaunchAgents`, `KeepAlive` true), with the
  settings in `EnvironmentVariables`.
- Windows: Task Scheduler, trigger "At log on", action `speedtest-collector.exe daemon`; set the variables
  with `setx` first. Or run `speedtest-collector.exe run` every 2 hours.
