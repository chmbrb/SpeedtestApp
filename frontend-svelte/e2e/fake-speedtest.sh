#!/bin/sh
# A stand-in for the Ookla CLI, for the browser tests. It prints the same `--format=jsonl` shape as the real tool
# (testStart, ping, download and upload readings with a progress value, then a result), takes about 7 seconds, and
# always reports 100 Mbps down and 20 Mbps up. It never contacts any network.
#
# Create the file named by $E2E_FAIL_FLAG (default /tmp/e2e-fake-speedtest-fail) to make the next run fail like a real
# network problem would.
FLAG="${E2E_FAIL_FLAG:-/tmp/e2e-fake-speedtest-fail}"
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

echo '{"type":"testStart","timestamp":"'"$(now)"'","isp":"Fake ISP","server":{"id":1,"host":"fake.example.test","port":8080,"name":"Fake Server","location":"Testville, ZZ","country":"Testland","ip":"198.51.100.1"}}'
if [ -e "$FLAG" ]; then
  echo "[error] Cannot read: Network is unreachable" >&2
  exit 1
fi
for p in 0.2 0.4 0.6 0.8 1.0; do
  echo '{"type":"ping","timestamp":"'"$(now)"'","ping":{"jitter":0.5,"latency":12.5,"progress":'"$p"'}}'; sleep 0.2
done
i=1
while [ $i -le 20 ]; do
  bw=$((i * 625000)); prog=$(awk "BEGIN{print $i/20}")
  echo '{"type":"download","timestamp":"'"$(now)"'","download":{"bandwidth":'"$bw"',"bytes":'"$((i * 100000))"',"elapsed":'"$((i * 150))"',"progress":'"$prog"'}}'
  sleep 0.15; i=$((i + 1))
done
i=1
while [ $i -le 15 ]; do
  bw=$((i * 166666)); prog=$(awk "BEGIN{print $i/15}")
  echo '{"type":"upload","timestamp":"'"$(now)"'","upload":{"bandwidth":'"$bw"',"bytes":'"$((i * 50000))"',"elapsed":'"$((i * 150))"',"progress":'"$prog"'}}'
  sleep 0.15; i=$((i + 1))
done
echo '{"type":"result","timestamp":"'"$(now)"'","ping":{"jitter":0.5,"latency":12.5,"low":11.9,"high":14.0},"download":{"bandwidth":12500000,"bytes":2000000,"elapsed":3000,"latency":{"iqm":20.1,"low":11.0,"high":40.0,"jitter":2.0}},"upload":{"bandwidth":2500000,"bytes":750000,"elapsed":2250,"latency":{"iqm":15.0,"low":11.0,"high":30.0,"jitter":1.0}},"packetLoss":0,"isp":"Fake ISP","interface":{"internalIp":"192.0.2.10","name":"eth0","macAddr":"00:00:5E:00:53:01","isVpn":false,"externalIp":"203.0.113.7"},"server":{"id":1,"host":"fake.example.test","port":8080,"name":"Fake Server","location":"Testville, ZZ","country":"Testland","ip":"198.51.100.1"},"result":{"id":"11111111-2222-4333-8444-555555555555","url":"https://www.speedtest.net/result/c/11111111-2222-4333-8444-555555555555","persisted":true}}'
