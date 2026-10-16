# 設計決策紀錄

規格未涵蓋、或實作時需要取捨的細節，都記錄於此。每項決策皆選擇「最簡單且與規格一致」的做法。

## 環境與工具

0. **全部 Docker 化**：主機只需要 Docker 與 make。`Dockerfile` 是多階段建置（`server`、`worker`、`tools`、`web`），
   `docker-compose.yml` 含執行服務（postgres、migrate、server、worker、web）與以 profile 區分的工具容器
   （`tools`：seed／reconcile／demo-race／k6prep／goose；`dev`：go、go-tc、sqlc、node；`bench`：k6）。
   Go 測試在官方 `golang` 映像中執行（內含 gcc，所以 `-race` 不需另外裝編譯器）；sqlc、k6 也都是官方映像。
   容器以 root 執行開發工具，因此由容器寫出的檔案（例如 `make sqlc`）在主機上可能是 root 擁有。
   正式執行的 server、worker、tools 映像以非 root 使用者（uid 10001）執行。
   nginx 提供建置後的前端並把 `/api` 代理到 `server`（以 Docker DNS 在請求時解析，不要求啟動順序）。
   `worker` 在尚未 seed（沒有任何物品模板）時不會建立空回合，避免白白用掉一個刷新間隔。
1. **測試資料庫來源**：整合測試預設以 testcontainers-go 啟動 `postgres:16` 容器（`make test-tc`，
   經 Docker socket，已驗證通過）。`make test` / `make test-race` 則設定 `TEST_DATABASE_URL` 指向 compose 的
   PostgreSQL，每個測試套件在其上建立專屬的臨時資料庫並於結束時刪除，速度較快。兩種模式使用同一份遷移檔與同一份測試程式。
2. **遷移檔同時被 goose CLI 與 Go 程式使用**：`migrations/embed.go` 以 `go:embed` 內嵌 SQL，
   測試與 `cmd/demo-race` 透過 goose library 套用；`make migrate` 則使用 goose CLI。
3. **新增的套件**（規格目錄未列出，但為必要）：
   - `internal/invariants`：`CheckInvariants`，供測試與 `cmd/reconcile` 共用同一份實作。
   - `internal/testutil`：測試資料庫與測試資料（fixtures），純 SQL，不依賴業務套件，避免循環引用。
   - `cmd/seed`：`make seed` 的實作。
4. **README 使用繁體中文**（規格只規定 `docs/` 下為繁中；README 面向使用者，故同樣使用繁中）。
   程式碼、註解、commit 訊息皆為英文。

## 資料庫

5. **append-only 於資料庫層強制**：`bids`、`gold_ledger` 以 `BEFORE UPDATE OR DELETE` 觸發器拒絕修改，
   而不只是約定。測試以 `TRUNCATE` 重置資料，不觸發這些列級觸發器。
6. **`level` 與金額欄位的額外 CHECK**：`start_price > 0`、`current_price >= start_price`、`bids.amount > 0`、
   `base_value > 0`，用於在資料庫層擋住明顯錯誤的資料。
7. **帳本重複寫入的處理**：`INSERT ... ON CONFLICT DO NOTHING`，影響列數為 0 時回傳 `ErrDuplicateEntry`，
   由呼叫端的交易回滾，避免餘額被更新兩次。

## 規則細節

8. **起標價**以整數運算 `base * (20 + level) / 20`（向下取整），等同 `base * (1 + level/20)` 取整數，且不使用浮點數。
9. **物品生成**：每個區間 `b` 的物品等級範圍為 `[10b, 10b+9]`（區間 0 為 `[1, 9]`，不存在 0 級物品）。
   候選模板為 `min_level <= 區間上限` 者，先洗牌後不重複抽取 `ITEMS_PER_BRACKET` 件（候選不足時才循環重用）；
   物品等級在 `[max(區間下限, 模板 min_level), 區間上限]` 內均勻隨機。
10. **模板資料**：32 個模板（8 種類型 × 4 個階級，min_level 為 1、15、30、50，涵蓋 1 到 50），名稱為繁體中文。
11. **結標時間由資料庫計算**：建立拍賣時傳入隨機持續毫秒數，SQL 以 `now() + duration` 計算 `end_at`；
    Go 端的亂數只決定持續時間，不使用 `time.Now()`。
12. **時間區間**：SQL 以 `end_at - now()` 計算剩餘毫秒（下限 0），Go 端以純函式 `Bands.Of` 換算為區間。
    API 只回傳區間，不回傳剩餘毫秒或 `end_at`。
13. **拍賣列表排序**：`sort` 支援 `ending`（預設，依結標時間由近到遠，只洩漏相對順序，不洩漏時間）、
    `price_asc`、`price_desc`、`level_desc`。

## 出價與結算

14. **「是否已結標」在出價時使用 `clock_timestamp()`**：規格要求以資料庫時間判斷。`now()` 在 PostgreSQL 中等於
    「交易開始時間」，若出價交易在等待拍賣列鎖的期間跨過結標時間，`now()` 仍是舊值，會讓結標後的出價被接受。
    因此取得列鎖後，在同一個查詢中以 `clock_timestamp() < end_at` 判斷；仍然是資料庫時鐘，只是取「當下」而非「交易開始」。
    `bids.created_at` 也使用這個同一時間點，所以「所有出價的 `created_at` 都早於 `end_at`」可以被精確驗證。
    結算端（`end_at <= now()`）依規格使用 `now()`：結算交易的第一個查詢緊接著開始，且結算與出價共用同一把拍賣列鎖，
    不會產生不一致。
15. **冪等鍵重放**：先查一次；取得拍賣列鎖後再查一次（避免並發重送時第二個請求先被
    `ErrAlreadyTopBidder` 擋下而不是回傳原結果）；`INSERT ... ON CONFLICT DO NOTHING` 作為最後防線。
    重放一律回傳原本的出價結果，不重新驗證；同一把鍵若被用在不同拍賣，仍回傳最初那筆結果。
16. **結算批次的鎖**：規格的第一步查詢（`FOR UPDATE SKIP LOCKED`）只用來挑出待結算的 id，
    其鎖在該語句的交易結束時釋放；每場拍賣再於獨立交易中以 `FOR UPDATE SKIP LOCKED` 重新鎖定並再次確認
    `status = 'active' AND end_at <= now()`。若讓外層交易持鎖，內層交易會等待自己外層的鎖而卡死。
17. **前一位出價者的出價**：因為鎖定拍賣列後，該拍賣最新的一筆 bid 必定是目前最高出價，
    解凍時以 `ORDER BY id DESC LIMIT 1` 取得該筆 bid 的 id 與金額，並驗證金額等於 `current_price`。
18. **驗證順序**：依規格為「狀態／是否結標 → 是否為最高出價者 → 等級區間 → 金額」。額外的 `ErrUserNotFound`
    （玩家不存在）與 `ErrMissingIdempotencyKey` 也是 sentinel error。玩家等級在驗證時不加鎖讀取
    （本里程碑沒有升級機制，等級不會改變）。
19. **無人出價流標**：`status = 'expired'`，物品維持無主，不寫 inbox、不寫帳本。
20. **inbox 訊息**：結算時寫入英文訊息（含拍賣編號與價格）；前端使用結構化欄位（物品、花費、時間）而不是顯示這段文字。
    「花費」由 inbox 關聯到 `auctions.current_price` 取得（結算後不再變動）。

## 刷新與 worker

21. **刷新排程由資料庫決定**：worker 每秒檢查一次，`RefreshIfDue` 在交易內取得 advisory lock，
    僅當「最新一輪的 `started_at` 距今 ≥ `REFRESH_INTERVAL`（資料庫時鐘）」或尚無任何回合時才建立新一輪。
    因此重啟 worker、或同時開多個 worker，都不會在同一個間隔內建立兩輪。
    `POST /api/dev/refresh` 與 `make seed` 則無條件刷新一輪。
22. **結算迴圈**：每 `SETTLE_INTERVAL` 執行一次；若回傳的批次是滿的，立即再跑，直到清空才等下一個間隔。
    單場結算失敗不會中斷整批，錯誤會彙整後記錄。

## HTTP API

23. **身分**：僅 `APP_ENV=dev` 時讀取 `X-Dev-User-Id`；非 dev 時 `/api/me*` 與出價一律回 401，
    因為本里程碑沒有真正的驗證。`/api/users` 與 `/api/dev/*` 在非 dev 時完全不註冊（404）。
    瀏覽拍賣（列表、單場）不需要身分；沒有身分時 `is_mine` / `i_have_bid` 為 false。
24. **列表預設列出所有區間**：「預設為目前玩家區間」是前端行為（以玩家所屬區間作為篩選初始值）；
    API 在沒有 `bracket` 參數時回傳所有區間的進行中拍賣。
25. **出價請求本文**：`{"amount": <整數>}`；浮點數、字串、缺少欄位或多餘資料一律 400 `invalid_request`。
    `Idempotency-Key` 最長 200 字元。成功回 200（重放時 `replayed: true`），不使用 201。
26. **拍賣列表與單場回應多一個 `has_bids`**（規格欄位清單已包含）。已結算／流標的拍賣不出現在列表，但仍可由 `GET /api/auctions/{id}` 取得。
27. **錯誤碼**：`bid_too_low`、`already_top_bidder`、`auction_ended`、`insufficient_gold`、`level_bracket_mismatch`、
    `auction_not_found`、`missing_idempotency_key`、`invalid_request`、`unauthorized`、`not_found`、`internal_error`。
    HTTP 方法不符的請求也回 JSON 格式的 404（沒有 405）。
28. **帳本分頁**：以 `before_id`（keyset）分頁，`limit` 預設 50、最大 200；回應含 `next_before_id`（沒有下一頁時為 null）。

## 前端

29. **玩家選擇只存在 React state**（重新整理後回到第一位玩家），符合「選擇結果存在 React state」。
    兩個瀏覽器分頁因此可以各自扮演不同玩家。
30. **出價輸入框**：玩家沒有手動編輯時，輸入框會跟著輪詢到的 `min_next_bid` 更新；
    編輯後保持玩家輸入，直到出價成功或收到 `bid_too_low`（此時改為新的 `min_next_bid`）。
31. **玩家只能對自己區間出價**：前端在區間不符時停用出價按鈕並說明原因；後端仍會以
    `level_bracket_mismatch`（422）擋下。輪詢使每 2 秒重新抓取拍賣列表、`/api/me` 與包裹。
32. **等級區間選單固定為 0–5**（對應預設 `BRACKET_MIN=0`、`BRACKET_MAX=5`）。

## 測試與壓測

33. **併發測試的有效性以「突變」驗證**：把 `LockUsers` 的排序拿掉，死結測試立即出現
    `deadlock detected (SQLSTATE 40P01)`；把出價的結標判斷由 `clock_timestamp()` 改回 `now()`，
    結標瞬間測試會抓到結標後被接受的出價。兩者驗證後都已還原。
34. **`-race` 與 cgo**：`go test -race` 需要 C 編譯器；`go` 容器使用官方 `golang` 映像（含 gcc）並設定 `CGO_ENABLED=1`。
35. **k6 煙霧測試使用獨立資料庫與獨立 server 容器**：`k6/run-smoke.sh` 透過 `cmd/k6prep` 建立臨時資料庫
    （50 位同區間玩家＋一場拍賣），啟動另一個 `server` 容器（`ah-smoke-server`）連到它，以 `grafana/k6` 容器跑測試，
    跑完後對該資料庫執行 reconcile 並刪除資料庫。
    這樣不會污染開發資料，也讓數字可重現。
36. **煙霧測試的出價增額為線性（`BID_INCREMENT_PERCENT=0`、`BID_INCREMENT_MIN=10`）**：預設 5% 的增額讓價格
    指數成長，約數百筆成功出價後就超過任何玩家的餘額，之後只剩「金幣不足」。為了讓 30 秒內持續測到真實的成功出價路徑，
    壓測使用線性增額；數字與影響見 `docs/benchmarks.md`。
