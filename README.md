# Auction House（拍賣行）— Milestone 1

類似網頁遊戲 Gladiatus 拍賣行的競標系統，也是一個學習高併發設計的專案。
本里程碑的目標是 **規則正確、金幣絕不錯帳、併發下不出錯**；效能與即時性不是這個階段的目標。

- 後端：Go 1.22+、PostgreSQL 16、pgx/v5 + sqlc、goose、`net/http`
- 前端：React + TypeScript + Vite、TanStack Query、Tailwind CSS（正式環境由 nginx 提供）
- 測試：testcontainers-go（真實 PostgreSQL）、`go test -race`、k6
- **全部在 Docker 內執行**：主機只需要 Docker 與 `make`，不用安裝 Go、Node、goose、sqlc、k6、C 編譯器或 PostgreSQL。

不在本里程碑範圍：Redis、WebSocket、訊息佇列、真正的登入驗證、限流、ORM。

## 從零啟動

先決條件：Docker（含 Compose v2）與 `make`（Debian/Ubuntu：`sudo apt install make`）。

```bash
cp .env.example .env     # 選用；不複製就使用預設值
make up                  # 建置映像，啟動 PostgreSQL → 遷移 → server、worker、web，等到健康為止
make seed                # 6 位玩家（各 10000 金幣）、32 個物品模板、第一輪拍賣
```

打開 <http://localhost:5173>。用兩個瀏覽器分頁各選一位同區間的玩家（例如「卡爾 23 級」與「黛安 24 級」，都在 20–29 級），
就能完整玩一輪：出價、被超越（金幣立刻退回）、結標得標、在「包裹」看到物品。

`make up` 會啟動這些容器：

| 服務 | 說明 | 埠 |
| --- | --- | --- |
| `postgres` | PostgreSQL 16，資料存在 volume `pgdata` | 5432 |
| `migrate` | 一次性：套用 goose 遷移後結束 | — |
| `server` | API（`cmd/server`） | 8080 |
| `worker` | 每 2 秒結算、依間隔刷新（`cmd/worker`） | — |
| `web` | nginx 提供建置後的前端，並把 `/api` 代理到 `server` | 5173 |

埠可在 `.env` 以 `WEB_PORT`、`API_PORT`、`POSTGRES_PORT` 修改。`make down` 停止（保留資料），`make clean` 連資料庫 volume 一起刪除。

### 快速試玩（縮短拍賣時間）

預設拍賣長度是 3–25 分鐘。要縮短等待，在 `.env` 設定後 `make up`（會重建並重啟服務）：

```
AUCTION_MIN_DURATION=40s
AUCTION_MAX_DURATION=90s
BAND_VERY_SHORT=15s
BAND_SHORT=30s
BAND_MEDIUM=45s
BAND_LONG=60s
```

要立刻再刷新一輪：`POST http://localhost:8080/api/dev/refresh`（僅 `APP_ENV=dev`）。

## 設定（環境變數）

完整清單見 [.env.example](.env.example)，compose 會自動讀取 `.env`，每個值都有預設。重點：

| 變數 | 預設 | 說明 |
| --- | --- | --- |
| `APP_ENV` | `dev` | 只有 `dev` 才啟用 `X-Dev-User-Id`、`/api/users`、`/api/dev/*` |
| `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` | `auction` | 容器內的 `DATABASE_URL` 由這三個組成 |
| `BID_INCREMENT_PERCENT` / `BID_INCREMENT_MIN` | `5` / `10` | 最低下一口價 = 現價 + max(ceil(現價×%), 最小增額) |
| `BAND_VERY_SHORT` … `BAND_LONG` | `2m 5m 10m 20m` | 時間區間門檻 |
| `AUCTION_MIN_DURATION` / `AUCTION_MAX_DURATION` | `3m` / `25m` | 拍賣長度，均勻隨機 |
| `SETTLE_INTERVAL` / `SETTLE_BATCH_SIZE` | `2s` / `50` | 結算頻率與批次大小 |
| `REFRESH_INTERVAL` / `ITEMS_PER_BRACKET` | `5m` / `8` | 刷新頻率與每區間物品數 |

## 常用指令

| 指令 | 說明 |
| --- | --- |
| `make up` / `make down` / `make clean` | 啟動／停止／停止並刪除資料 |
| `make ps` / `make logs` | 服務狀態／追蹤記錄 |
| `make migrate` / `make migrate-down` | 套用／回退一個遷移（`up` 已自動套用） |
| `make sqlc` | 由 `queries/` 重新產生 `internal/db`（sqlc 容器） |
| `make seed` | 種子資料（已有玩家時不做事） |
| `make server` / `make worker` / `make web` | 在前景執行單一服務（看記錄） |
| `make web-dev` | Vite 熱重載開發伺服器 <http://localhost:5174>（API 代理到 `server`） |
| `make test` | Go 單元 + 整合 + 併發測試（連 compose 的 PostgreSQL） |
| `make test-race` | `go test -race -count=10 ./...` |
| `make test-tc` | 同樣的測試，但改用 testcontainers-go 啟動自己的 `postgres:16`（經 Docker socket） |
| `make web-test` | 前端型別檢查與單元測試 |
| `make reconcile` | 檢查所有不變量，違反時以非零狀態結束 |
| `make demo-race` | 不安全出價示範，印出被打破的不變量 |
| `make k6-smoke` | 50 VU 對同一場拍賣出價 30 秒，之後自動 reconcile |

## 測試

```bash
make test           # 整合測試使用 compose 的 PostgreSQL（TEST_DATABASE_URL 指向它）
make test-race      # -race，重複 10 次
make test-tc        # testcontainers-go 預設模式：每個測試套件啟動自己的 postgres:16 容器
```

`make test` / `make test-race` 在 `go` 容器內執行；整合測試在 compose 的 PostgreSQL 上建立並刪除自己的臨時資料庫，
不會動到 `auction` 資料庫。`-race` 所需的 C 編譯器已內含在 `golang` 映像中。

每個併發測試結束都會呼叫 `CheckInvariants`。主要併發情境（`tests/`）：

- 100 個 goroutine 對同一場拍賣隨機出價，最終價格等於成功出價的最大值
- 兩位玩家在多場拍賣中交錯互相超越（沒有鎖順序時會 `deadlock detected`）
- 同一個 Idempotency-Key 並發重送 20 次，只產生一筆出價
- 3 個結算 worker 同時處理同一批已結標拍賣，沒有重複結算
- 結標瞬間持續出價：結標後沒有任何出價被接受，結算正確

### 教學示範：不安全的出價

```bash
make demo-race
```

`cmd/demo-race` 用「讀取後再寫入、不加鎖、不用交易」實作出價，30 位玩家併發對同一場拍賣出價，
結束後印出被打破的不變量（餘額與帳本不一致、被超越者的金幣沒退、最高價不等於凍結金額…）。
它在自己建立的臨時資料庫中執行，不會動到開發資料，也不會被正式程式碼引用。

## 核心設計（為什麼金幣不會錯帳）

1. **所有餘額變動都在同一個交易內寫帳本**：`internal/wallet` 的 `Grant / Freeze / Unfreeze / Spend`
   同時更新餘額與 `gold_ledger`（只追加，資料庫觸發器禁止 UPDATE／DELETE）。
2. **出價是一個交易**：先 `SELECT … FOR UPDATE` 鎖拍賣列（同一場拍賣的出價因此被序列化），
   再依 user_id 由小到大鎖玩家列（避免兩位玩家互相超越時死結），然後插入出價、凍結新出價者、
   解凍前一位出價者、更新價格。任何一步失敗全部回滾。
3. **時間以資料庫為準**：是否結標、剩餘時間、結標時間都在 SQL 中以資料庫時鐘計算；
   Go 程式不用 `time.Now()` 做業務判斷。`end_at` 絕不回傳給前端，只回傳時間區間。
4. **冪等**：`(user_id, Idempotency-Key)` 唯一；重送回傳原結果，不會重複凍結金幣。
5. **結算可重入、可多 worker 並行**：每場拍賣在獨立交易中以 `FOR UPDATE SKIP LOCKED` 鎖定並重新確認狀態。
6. **隨時可對帳**：`make reconcile` 在一致的快照中檢查 5 組不變量（餘額＝帳本、無負餘額、金幣守恆、
   進行中拍賣的凍結金額、已結算拍賣的物品與扣款）。

更多取捨見 [docs/decisions.md](docs/decisions.md)。

## 目錄

```
Dockerfile        多階段建置：server、worker、tools、web
docker-compose.yml 服務與開發工具容器（profiles: tools、dev、bench）
docker/           nginx.conf、migrate.sh
cmd/server/       API 伺服器
cmd/worker/       背景工作者（結算與刷新）
cmd/reconcile/    帳本對帳工具（共用 internal/invariants 的檢查）
cmd/demo-race/    故意不安全的出價示範（教學用）
cmd/seed/         種子資料
cmd/k6prep/       k6 煙霧測試的獨立資料庫準備工具
internal/auction/ 拍賣規則、出價、結算、檢視
internal/wallet/  金幣凍結、解凍、扣款與帳本
internal/item/    物品模板與回合刷新
internal/invariants/ CheckInvariants（測試與 reconcile 共用）
internal/httpapi/ HTTP handler 與路由
internal/config/  環境變數設定
internal/db/      sqlc 產生的程式碼 + 連線／交易輔助
internal/testutil/ 測試資料庫與測試資料
queries/ migrations/ SQL 查詢與 goose 遷移
tests/            併發測試（出價、結算、結標瞬間）
web/              React 前端
k6/               壓測腳本
docs/             decisions.md（設計決策）、benchmarks.md（基準數字）
```

## HTTP API 摘要

開發用身分：以標頭 `X-Dev-User-Id` 指定玩家（僅 `APP_ENV=dev`）。

| 方法與路徑 | 說明 |
| --- | --- |
| `GET /api/users` | 玩家清單（dev） |
| `GET /api/auctions?bracket=&type=&sort=` | 拍賣列表（預設只列進行中） |
| `GET /api/auctions/{id}` | 單場拍賣 |
| `POST /api/auctions/{id}/bids` | 出價 `{"amount": 1103}`，標頭 `Idempotency-Key` 必填 |
| `GET /api/me`、`/api/me/inbox`、`/api/me/ledger` | 玩家資料、包裹、帳本（最新在前，`limit` / `before_id` 分頁） |
| `POST /api/dev/refresh` | 立即刷新一輪（dev） |

出價錯誤：`bid_too_low` 409（附 `min_next_bid`）、`already_top_bidder` 409、`auction_ended` 410、
`insufficient_gold` / `level_bracket_mismatch` 422、`auction_not_found` 404、缺少冪等鍵 400。
錯誤格式：`{"error": {"code": "bid_too_low", "message": "...", "min_next_bid": 1103}}`。

## 基準測試

見 [docs/benchmarks.md](docs/benchmarks.md)。
