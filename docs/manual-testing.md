# pacchain 手動測試清單

用途：發布前，或修改到路由、failover、重新載入、log 這類核心邏輯之後，親手確認一次 pacchain 的行為。

自動化測試（`make test`，以及 GitHub Actions）負責檢查邏輯是否正確。這份清單檢查的是自動化測試沒辦法回答的問題：**實際用起來長什麼樣、錯誤訊息看不看得懂、接上 Burp 和真實 STG 環境後能不能正常運作。**

> 這份文件會放在公開的 repo 裡。範例一律使用 `.test` 保留網域。真實環境（K 區）用 `<STG host>` 這類佔位符表示，**不要把真實的 host、proxy 或 PAC 內容寫進來。**

---

## 0. 準備

```bash
cd ~/Desktop/CyberCoder/PacChain/pacchain
git pull && make build
```

開三個終端機：

| 終端機 | 用途 | 指令 |
|---|---|---|
| T1 | 假 STG proxy | `make devenv` |
| T2 | pacchain | 各項測試會寫要怎麼啟動 |
| T3 | 送請求 | 先貼上下面的輔助指令 |

T3 的輔助指令：

```bash
P=./bin/pacchain
# who URL : 顯示狀態碼，以及實際由誰回應(fake upstream 的名字 / pacchain 標記的來源)
who() { curl -sk -o /dev/null -D - -x http://127.0.0.1:8888 "$@" | grep -iE '^(HTTP|x-fake-upstream|x-pacchain-upstream)' | tr -d '\r'; }
# raw host:port : 手動送 CONNECT，可以看到 pacchain 回應的完整內容(curl 遇到 CONNECT 失敗時不會顯示內容)
raw() { printf 'CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n' "$1" "$1" | nc -w 2 127.0.0.1 8888; }
```

HTTPS 網址用 `who` 時會看到兩行狀態：第一行 `HTTP/1.0 200 OK` 是 pacchain 回覆 CONNECT 的結果（tunnel 建好了）；第二行 `HTTP/2 200` 才是伺服器的真正回應。

`examples/dev.pac` 裡各網域對應的情境：

| 網域 | 走到哪裡 | 模擬的情況 |
|---|---|---|
| `*.a.stg.test` | 18431 stg-a | 正常 |
| `*.b.stg.test` | 18432 stg-b | 正常 |
| `*.deny.test` | 18407 | upstream 回 407 |
| `*.badgw.test` | 18502 | upstream 回 502 |
| `*.hangup.test` | 18000 | upstream 接通後直接掛斷 |
| `*.down.test` | 18999（沒有程式在聽） | proxy 連不上（等同 VPN 斷線） |
| `*.failover.test` | 18999，再試 18432 | 第一個 proxy 壞掉 |
| `*.fallback.test` | 18999，再試 DIRECT | proxy 壞掉後退回直連 |
| `*.loop.test` | PAC 進入無限迴圈 | PAC 本身寫錯 |
| 其他 | DIRECT | 不在 PAC 規則內的網站 |

除非該項另外說明，T2 一律這樣啟動：`$P -c examples/dev.pac`

---

## A. 啟動與命令列

### A1 版本
**為什麼**：發布或回報問題時，要能確認目前跑的是哪個版本。
```bash
$P --version
```
**預期**：`pacchain 0.1.0-dev`，exit code 0
**自動測試**：`TestVersionFlag`

### A2 沒有給 `-c`
**為什麼**：參數用錯時，要清楚說明缺了什麼，不能直接 panic。
```bash
$P; echo "exit=$?"
```
**預期**：先印出 `Missing required flag -c`，接著是使用說明，最後 `exit=2`
**自動測試**：`TestMissingPACIsUsageError`

### A3 PAC 檔案不存在
**為什麼**：原版只有加 `-v` 才看得到錯誤，或者直接 panic。檔名打錯時，一定要讓人看得到。
```bash
$P -c nope.pac; echo "exit=$?"
```
**預期**：`[-] load PAC from nope.pac: open nope.pac: no such file or directory`，`exit=1`
**自動測試**：`TestMissingPACFileFailsVisibly`

### A4 正常啟動
**為什麼**：預設 port 不能跟 Burp 的 8080 衝突；啟動時也要讓使用者知道修改 PAC 檔會自動生效。
```bash
$P -c examples/dev.pac        # 在 T2 執行
```
**預期**：
```
[*] pacchain 0.1.0-dev listening on 127.0.0.1:8888 (PAC: examples/dev.pac)
[*] watching examples/dev.pac for changes: edit it or switch its symlink, no restart needed
```
**自動測試**：`TestDefaultListenIsLoopback8888`、`TestStartupAnnouncesWatchedFile`

### A5 port 已被佔用
**為什麼**：如果 pacchain 自己默默換到別的 port，Burp 的流量會送到佔用 8888 的那個程式（可能是另一個還在跑舊 PAC 的 pacchain）。所以 port 被佔用時一定要報錯。
```bash
$P -c examples/dev.pac; echo "exit=$?"     # T2 已經有一個在跑，再開一個
```
**預期**：`[-] cannot listen on 127.0.0.1:8888: port already in use (Burp, Charles, Fiddler or another pacchain?). Choose another port with -l`，`exit=1`
**自動測試**：`TestListenPortInUseFails`

### A6 監聽在對外的位址
**為什麼**：任何連得到這個 port 的人，都能透過你的 VPN 和 STG proxy 送流量。
```bash
$P -c examples/dev.pac -l 0.0.0.0:8899     # 看完 Ctrl+C
```
**預期**：第一行是 `[!] 0.0.0.0:8899 is reachable from other machines: ...`
**自動測試**：`TestIsLoopbackListen`

### A7 `-r` 試算路由
**為什麼**：開始測試前先確認某個網址會走哪個 proxy。HTTPS 網址的 path 在實際流量中根本到不了 pacchain，所以試算時也要先去掉 path，結果才會和實際一致。
```bash
$P -c examples/dev.pac -r 'https://app.a.stg.test/login?next=/admin'
$P -c examples/dev.pac -r 'https://x.failover.test/'
$P -c examples/dev.pac -r 'app.a.stg.test'; echo "exit=$?"
```
**預期**：
1. `PROXY 127.0.0.1:18431`
2. 兩行：`PROXY 127.0.0.1:18999`、`PROXY 127.0.0.1:18432`
3. `[-] -r needs a full URL such as https://app.a.stg.test/`，`exit=2`

**自動測試**：`TestResolveFlag`、`TestResolveUsesBrowserURL`、`TestResolveFlagRejectsURLWithoutScheme`

---

## B. 路由

### B1 HTTPS 依照 PAC 分流
**為什麼**：這是 pacchain 最核心的功能。
```bash
who https://app.a.stg.test/
who https://app.b.stg.test/
```
**預期**：第一個 `x-fake-upstream: stg-a`，第二個 `x-fake-upstream: stg-b`。T1 和 T2 也各會出現一行對應的 `[+]`。
**自動測試**：`TestPACRoutesToSelectedUpstream`

### B2 HTTP 依照 PAC 分流
**為什麼**：HTTP 和 HTTPS 在程式裡是兩條不同的路徑，兩條都要確認。
```bash
who http://web.a.stg.test/
```
**預期**：`X-Fake-Upstream: stg-a`；T2 顯示 `[+] GET http://web.a.stg.test/ → PROXY 127.0.0.1:18431 (dev.pac, ...) 200`
**自動測試**：`TestPACRoutesToSelectedUpstream`

### B3 DIRECT 不經過任何 proxy
**為什麼**：不在 PAC 規則內的網站，應該直接連線。
```bash
who https://example.com/
```
**預期**：狀態碼 200，沒有 `x-fake-upstream`；T1 **沒有**新的 log；T2 顯示 `→ DIRECT`
**自動測試**：`TestPACDirectBypassesUpstreams`

### B4 `dnsDomainIs` 和瀏覽器一樣是後綴比對
**為什麼**：我們決定保留瀏覽器的行為（`evilstg.test` 也會符合 `stg.test`），這樣 pacchain 的路由才會和瀏覽器完全一致。
```bash
$P -c 'function FindProxyForURL(u,h){ return dnsDomainIs(h,"stg.test") ? "PROXY 127.0.0.1:18431" : "DIRECT"; }' -r https://evilstg.test/
```
**預期**：`PROXY 127.0.0.1:18431`
**自動測試**：`TestDnsDomainIsSuffixMatchesLikeBrowsers`

---

## C. 失敗處理

### C1 第一個 proxy 連不上 → 自動換下一個
**為什麼**：PAC 給了備援選項時，要跟瀏覽器一樣依序嘗試。
```bash
who https://x.failover.test/
```
**預期**：`x-fake-upstream: stg-b`。T2 會先出現 `[!] ... 18999 failed: ... (proxy unreachable: is the VPN connected?), trying next option`，再出現 `[+] ... → PROXY 127.0.0.1:18432`
**自動測試**：`TestFailoverWhenFirstProxyUnreachable`

### C2 換 proxy 重試時，POST body 要完整送出
**為什麼**：body 只能讀一次，重試時如果沒有重新送，表單資料就會遺失。
```bash
curl -s -D - -o /dev/null --data hello -x http://127.0.0.1:8888 http://web.failover.test/form | grep -i x-fake
```
**預期**：`X-Fake-Body-Bytes: 5`、`X-Fake-Upstream: stg-b`
**自動測試**：`TestHTTPFailoverReplaysRequestBody`

### C3 upstream 回 407 → 原樣轉送，並標示來源
**為什麼**：STG proxy 回傳的錯誤頁面可能寫了真正的原因，所以不能被改寫；同時要標示是哪個 proxy 回的，而且不能自動換到別的 proxy（可能會換到另一個環境）。
```bash
raw x.deny.test:443
curl -sk -o /dev/null -x http://127.0.0.1:8888 https://x.deny.test/; echo "exit=$?"
```
**預期**：
- `raw`：`HTTP/1.1 407 Proxy Authentication Required`、`Proxy-Authenticate: ...`、`X-Pacchain-Upstream: PROXY 127.0.0.1:18407`，以及內容 `fakeupstream deny: proxy authentication required`
- curl：`exit=56`（`CONNECT tunnel failed, response 407`）
- T2：`[-] CONNECT x.deny.test:443 → PROXY 127.0.0.1:18407 failed: answered 407 Proxy Authentication Required`

**自動測試**：`TestUpstreamRejectionIsRelayedWithSource`、`TestNoFailoverWhenProxyAnswersWithError`

### C4 upstream 回 502 → 原樣轉送
```bash
raw x.badgw.test:443
```
**預期**：`HTTP/1.1 502 Bad Gateway`、`X-Pacchain-Upstream: PROXY 127.0.0.1:18502`，內容是 `fakeupstream badgw: cannot reach target`（這是 upstream 自己的說明）
**自動測試**：`TestNoFailoverWhenProxyAnswersWithError`

### C5 upstream 接通後掛斷 → 由 pacchain 自己回 502
**為什麼**：upstream 沒有回應任何東西，pacchain 就要自己說明發生了什麼事。
```bash
raw x.hangup.test:443
```
**預期**：`HTTP/1.1 502 Bad Gateway`，**沒有** `X-Pacchain-Upstream`，內容開頭是 `pacchain: could not reach x.hangup.test:443`；T2 顯示 `closed the connection without answering CONNECT`
**自動測試**：`TestFailoverWhenFirstProxyHangsUp`

### C6 proxy 連不上 → 提示 VPN
**為什麼**：在你的環境裡，proxy 連不上幾乎都是因為 VPN 斷了。
```bash
raw x.down.test:443
```
**預期**：內容最後一行是 `hint: proxy unreachable: is the VPN connected?`；T2 那行 `[-]` 的結尾也有同樣的提示
**自動測試**：`TestUnreachableProxyHintsVPN`、`TestDirectFailureHasNoVPNHint`

### C7 proxy 失敗後，預設不會退回 DIRECT
**為什麼**：STG 的網域如果改走 DIRECT，連到的就是 production，而且頁面看起來完全正常。
```bash
raw x.fallback.test:443
```
**預期**：`502`，內容說明 `DIRECT fallback blocked`，並提到 `--allow-direct-fallback`；T2 顯示 `[-] ... → DIRECT fallback blocked (use --allow-direct-fallback to permit)`
**自動測試**：`TestDirectFallbackBlockedByDefault`、`TestHTTPDirectFallbackBlockedByDefault`

### C8 `--allow-direct-fallback` 會放行，但會發出警告
```bash
# T2 改成這樣啟動
$P -c examples/dev.pac --allow-direct-fallback
# T3
who https://x.fallback.test/
```
**預期**：啟動時出現 `[!] --allow-direct-fallback: ...`；請求時先出現 `[!] ... 18999 failed ..., trying next option`，接著**嘗試 DIRECT**。因為 `.test` 網域不存在，最後會是 `[-] ... → DIRECT failed: ... no such host`，這代表 DIRECT 確實有被嘗試。
**自動測試**：`TestDirectFallbackAllowedWithFlag`、`TestDirectFirstIsNotAFallback`

---

## D. PAC 引擎的保護

### D1 PAC 卡住 3 秒後中止，不影響其他請求
**為什麼**：PAC 是別人寫的程式碼。原版遇到一個卡住的請求，所有請求都會跟著卡住。
```bash
time curl -sk -o /dev/null -x http://127.0.0.1:8888 https://x.loop.test/
who https://app.a.stg.test/
```
**預期**：第一個指令大約 3 秒後失敗，T2 顯示 `[-] CONNECT x.loop.test:443 → PAC error: PAC evaluation timed out after 3s`；第二個指令立刻回 `x-fake-upstream: stg-a`
**自動測試**：`TestFindProxyForURLTimesOut`、`TestPACTimeoutFailsOnlyThatRequest`、`TestStartTimesOutOnTopLevelLoop`

---

## E. PAC 自動重新載入

準備（在 T3 執行）：
```bash
mkdir -p /tmp/pacmanual && cd /tmp/pacmanual
printf 'function FindProxyForURL(url, host) {\n  if (dnsDomainIs(host, ".stg.test")) return "PROXY 127.0.0.1:18431";\n  return "DIRECT";\n}\n' > env-a.pac
sed 's/18431/18432/' env-a.pac > env-b.pac
ln -sfn env-a.pac current.pac
cd -
```
T2 改成：`$P -c /tmp/pacmanual/current.pac`

每一步操作完，**等 2 秒**再用 `who https://app.stg.test/` 確認。

| # | 操作（T3） | 為什麼要測 | 預期 `who` | 預期 T2 |
|---|---|---|---|---|
| E1 | `ln -sfn env-b.pac /tmp/pacmanual/current.pac` | 用 symlink 切換 env-a / env-b | `stg-b` | `[*] PAC reloaded (file changed) ...` |
| E2 | `echo 'function FindProxyForURL(u,h){ return ' > /tmp/pacmanual/env-b.pac` | 原版遇到壞掉的 PAC 會直接崩潰 | **仍然是** `stg-b` | `[!] PAC reload (file changed) failed, still using the previous PAC: ...` |
| E3 | `sed 's/18431/18432/' /tmp/pacmanual/env-a.pac > /tmp/pacmanual/env-b.pac` | 修好之後能再次載入 | `stg-b` | `[*] PAC reloaded` |
| E4 | `sed -i '' 's/18432/18431/' /tmp/pacmanual/env-b.pac` | 直接編輯檔案內容 | `stg-a` | `[*] PAC reloaded` |
| E5 | `kill -HUP $(pgrep -f 'pacchain -c /tmp/pacmanual')` | 手動觸發重新載入 | `stg-a` | `[*] PAC reloaded (SIGHUP)` |
| E6 | `mv /tmp/pacmanual/env-b.pac /tmp/x.pac`，等 3 秒，再 `mv /tmp/x.pac /tmp/pacmanual/env-b.pac` | 存檔過程中檔案會短暫消失 | `stg-a` | **只出現一次** `[!] cannot read PAC file ...; keeping the current PAC`，而且**不會** reload |

**自動測試**：`TestReloadSwitchesToNewPAC`、`TestReloadKeepsPreviousPACWhenNewOneIsBroken`、`TestWatchPACDetectsChanges`、`TestWatchPACIgnoresMissingFile`、`TestBrokenPACFileKeepsPreviousRules`、`TestLookupsDuringReloadNeverFail`

### E7 重新載入後，Burp 原本的長連線會被關閉（要用 Burp 測）
**為什麼**：Burp 會一直重用已經建立的 tunnel。不關掉的話，切換 PAC 之後，這些連線還是會走舊的路由。curl 每次都建立新連線，所以測不出這個問題。
1. 執行 `ln -sfn env-a.pac /tmp/pacmanual/current.pac`，等 2 秒
2. Burp 的 upstream 設成 `*` → `127.0.0.1:8888`，用 Burp 內建瀏覽器開 `https://app.stg.test/`，應該是 stg-a 的頁面顏色
3. 執行 `ln -sfn env-b.pac /tmp/pacmanual/current.pac`
4. T2 應該出現 `closed N open tunnel(s)`，而且 **N ≥ 1**
5. 回到 Burp 瀏覽器重新整理，應該變成 **stg-b 的顏色**

**自動測試**：`TestPACFileChangeReroutesAndClosesTunnels`

測試完清理：`rm -rf /tmp/pacmanual`

---

## F. Log

### F1 沒加 `-v` 時，一個請求只印一行
**為什麼**：Burp 一直有流量時，log 必須好讀，才看得到 `[-]`。
```bash
who https://app.a.stg.test/
```
**預期**：T2 只多出一行 `[+] CONNECT app.a.stg.test:443 → PROXY 127.0.0.1:18431 (dev.pac, 0.xms)`
**自動測試**：`TestQuietModeIsOneLinePerConnection`、`TestLogConnectDecision`

### F2 不記錄 query string、header、cookie
**為什麼**：log 很容易被截圖或貼到報告裡，而 token 和 cookie 都在這些地方。
```bash
curl -s -o /dev/null -H 'Cookie: SESSION=s3cr3t' -H 'Authorization: Bearer b3arer' \
     -x http://127.0.0.1:8888 'http://web.a.stg.test/page?token=SECRET123'
```
**預期**：T2 顯示 `GET http://web.a.stg.test/page → ...`，**完全看不到** `SECRET123`、`s3cr3t`、`b3arer`。加 `-v` 重跑一次，結果也一樣。
**自動測試**：`TestLogHTTPDecisionWithoutQueryString`、`TestLogNeverContainsHeaders`

### F3 `-v` 會多印細節，但不會印出 PAC 內容
**為什麼**：真實的 PAC 裡面列了所有內部網域。原版加了 `-v` 會把整份 PAC 印出來。
```bash
# T2:$P -c examples/dev.pac -v
who https://app.a.stg.test/
```
**預期**：啟動時印 `[*] PAC loaded (NNNN bytes)`，**沒有** PAC 內容；請求那行後面多了 `remote=127.0.0.1:18431`，tunnel 關閉時還會多一行 `[*] CONNECT ... closed after Xms (sent N B, received N KB)`
**自動測試**：`TestStartDoesNotLogPACSource`、`TestVerboseLogsTunnelClose`、`TestHTTPLoaderDoesNotLogQueryString`

---

## G. 透明轉送（HTTP 不能被改寫）

### G1 `Accept-Encoding` 原樣送到伺服器
**為什麼**：在 Repeater 修改 header 測伺服器時，伺服器收到的必須是你送出的內容。
```bash
curl -s -D - -o /dev/null -H 'Accept-Encoding: br' -x http://127.0.0.1:8888 http://web.a.stg.test/ | grep -i x-fake-accept
curl -s -D - -o /dev/null -H 'Accept-Encoding:'    -x http://127.0.0.1:8888 http://web.a.stg.test/ | grep -i x-fake-accept
```
**預期**：第一個是 `X-Fake-Accept-Encoding: br`，第二個是空的。原版兩個都會變成 `gzip`。
**自動測試**：`TestHTTPAcceptEncodingPassedThrough`

### G2 gzip 回應完全不被改寫
**為什麼**：Burp 看到的回應必須和伺服器送出的一模一樣。
```bash
curl -s -D a.hdr -o a.bin -H 'Accept-Encoding: gzip, br' -x http://127.0.0.1:8888  http://web.a.stg.test/gzip   # 經過 pacchain
curl -s -D b.hdr -o b.bin -H 'Accept-Encoding: gzip, br' -x http://127.0.0.1:18431 http://web.a.stg.test/gzip   # 直接連 upstream
cmp a.bin b.bin && echo "body 相同"
diff <(grep -v '^Date' a.hdr) <(grep -v '^Date' b.hdr) && echo "header 相同"
file a.bin; rm -f a.hdr b.hdr a.bin b.bin
```
**預期**：`body 相同`、`header 相同`、`a.bin: gzip compressed data`
**自動測試**：`TestHTTPGzipResponseRelayedUnchanged`、`TestHTTPGzipResponseRelayedUnchangedDirect`

---

## H. PAC 收到的 `url` 參數和瀏覽器一致

### H1 讓 PAC 自己印出收到的參數
**為什麼**：有些 PAC 會看 `url`（例如 `shExpMatch(url, "https://*")`），pacchain 傳的值必須跟 Chrome 一樣。
```bash
printf 'function FindProxyForURL(url, host) {\n  console.log("PAC got url=" + url + " host=" + host);\n  return "DIRECT";\n}\n' > /tmp/show.pac
# T2:$P -c /tmp/show.pac
curl -sk -o /dev/null -x http://127.0.0.1:8888 'https://app.test/login?x=1'
curl -sk -o /dev/null -x http://127.0.0.1:8888 'https://app.test:8443/'
curl -s  -o /dev/null -x http://127.0.0.1:8888 'http://web.test/p?q=1'
```
**預期**（看 T2 的 `PAC got` 那幾行；後面的 `no such host` 是正常的）：
```
PAC got url=https://app.test/ host=app.test
PAC got url=https://app.test:8443/ host=app.test
PAC got url=http://web.test/p?q=1 host=web.test
```
**自動測試**：`TestPACURLForConnectMatchesBrowser`、`TestPACURLForHTTPIsFullURL`、`TestPACSchemeMatchOnURL`、`TestBrowserPACURL`

> `console.log` 只能拿來除錯，它會印出完整的 URL。不要加進真實的 PAC。測試完執行 `rm /tmp/show.pac`。

---

## I. 閒置提醒與資源使用

### I1 啟動 30 秒都沒有流量時提醒
**為什麼**：之前在公司遇過 Burp 的 upstream 設定沒有生效，pacchain 卻什麼都沒說。
```bash
# T2 重新啟動 pacchain，30 秒內什麼都不做
```
**預期**：30 秒後出現**一行** `[!] no requests received in 30s. ... Upstream proxy servers needs Destination host * → 127.0.0.1:8888 ...`，之後不會再出現。如果啟動後 30 秒內有送過任何請求，就不會出現這行。
**自動測試**：`TestIdleReminderWhenNoTraffic`、`TestNoIdleReminderAfterTraffic`

### I2 閒置時幾乎不佔資源
**為什麼**：忘記關掉 pacchain 也不應該拖慢電腦。
```bash
ps -o rss=,time= -p $(pgrep -f 'pacchain -c') ; sleep 60 ; ps -o rss=,time= -p $(pgrep -f 'pacchain -c')
```
**預期**：記憶體大約 10 MB 左右，而且沒有持續增加；CPU time 幾乎不變。

---

## J. 接上 Burp 的完整流程

**為什麼**：前面都是用 curl 扮演 Burp，這裡要確認接上真的 Burp 之後也能正常運作。

| # | 步驟 | 預期 |
|---|---|---|
| J1 | Burp：Settings → Network → Connections → Upstream proxy servers，新增 `*` → `127.0.0.1:8888`。Burp 自己的 listener 維持 `127.0.0.1:8080`，Intercept 設成 off | — |
| J2 | Burp 內建瀏覽器開 `https://app.a.stg.test/` | 頁面是 stg-a 的顏色；Burp HTTP history 有這筆紀錄；T2 有 `[+]` |
| J3 | 開 `http://web.b.stg.test/` | stg-b 的顏色 |
| J4 | 開 `https://x.failover.test/` | stg-b 的顏色；T2 先 `[!]` 再 `[+]` |
| J5 | 開 `https://x.deny.test/`，把 Burp 畫面上顯示的內容記下來 | 記錄 Burp 怎麼呈現 upstream 的 407（用來決定 README 要怎麼寫） |
| J6 | 開 `https://x.down.test/`，同樣記下 Burp 的畫面 | 記錄 Burp 怎麼呈現 pacchain 自己的 502 |
| J7 | 在 Repeater 送 `GET /gzip`，Host 是 `web.a.stg.test`，走 HTTP | Response 有 `Content-Encoding: gzip`，而且 Burp 能正常解壓顯示 |

如果 J2 完全沒有流量：先看 T2 有沒有 `[!] no requests received` 的提醒，再用 Temporary project + Burp defaults 重新測一次。

---

## K. 真實環境（公司 Mac + VPN + 真實 PAC）

**為什麼**：假環境沒辦法驗證 split DNS、真實 STG proxy 的行為，以及實際的頁面顏色。

準備：`ln -sfn env-a.pac current.pac`，然後 `$P -c current.pac`，Burp 照 J1 設定。

| # | 步驟 | 預期 |
|---|---|---|
| K1 | `$P -c env-a.pac -r https://<STG host>/` | 和 env-a 裡的規則一致（例如 `PROXY <STG proxy>:<port>`） |
| K2 | Burp 瀏覽器開 `https://<STG host>/` | **STG 的顏色**；T2 顯示正確的 upstream |
| K3 | `ln -sfn env-b.pac current.pac`，重新整理 | T2 顯示 `PAC reloaded ... closed N`；路由改成照 env-b 的規則 |
| K4 | 開一個 PAC 規則以外的外部網站 | 正常載入，T2 顯示 `→ DIRECT` |
| K5 | **斷開 VPN**，重新整理 STG 頁面 | T2 顯示 `[-] ... (proxy unreachable: is the VPN connected?)`；記下 Burp 的畫面和等了多久 |
| K6 | 重新連上 VPN，重新整理 | 恢復正常，不需要重啟 pacchain |

---

## L. CI

| # | 步驟 | 預期 |
|---|---|---|
| L1 | 看 GitHub repo 的 Actions 分頁，或執行 `gh run list -R 0xnashi/pacchain --limit 3` | 最新的 commit 是 ✓，`ubuntu-latest` 和 `macos-latest` 都通過 |

---

## 結果記錄

| 區 | 項目 | 結果 | 備註 |
|---|---|---|---|
| A | A1–A7 啟動與命令列 | | |
| B | B1–B4 路由 | | |
| C | C1–C8 失敗處理 | | |
| D | D1 PAC 卡住 | | |
| E | E1–E7 重新載入 | | |
| F | F1–F3 Log | | |
| G | G1–G2 透明轉送 | | |
| H | H1 PAC url 參數 | | |
| I | I1–I2 閒置與資源 | | |
| J | J1–J7 Burp | | J5、J6 記下 Burp 的畫面 |
| K | K1–K6 真實環境 | | |
| L | L1 CI | | |
