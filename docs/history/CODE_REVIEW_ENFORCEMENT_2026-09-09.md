# Đánh giá cơ chế review & thiết kế thi hành — 2026-09-09

Ngày: 2026-09-09 · Phạm vi: toàn repo, `main` @ `2347c00`
Người lập: senior code-review · Nguồn: đo trực tiếp trên repo, không ước lượng
Ngôn ngữ: tiếng Việt (theo thoả thuận; `CODE_REVIEW.md` vẫn là chuẩn, tiếng Anh)

> **Tài liệu này không phải chuẩn thứ hai.** Chuẩn là `CODE_REVIEW.md`. Tài liệu này là
> bản đánh giá kèm phần thiết kế cho **lớp thi hành** — thứ `CODE_REVIEW.md` mô tả nhưng
> chưa có điểm bám nào trong repo. Các tài liệu cùng loại: `CODE_REVIEW_AUDIT.md`,
> `TEAM_QUALITY_BASELINE_2026-09-08.md`.

---

## 0. Kết luận

**AnyPick không thiếu chuẩn review — nó thiếu điểm mà chuẩn đó được thi hành.**

`CODE_REVIEW.md` (589 dòng) có đủ: phân loại rủi ro, thang P0–P3, thứ tự authority,
checklist theo vùng, quy tắc "finding phải thành gate", nhịp review hàng tháng. Viết
thêm một chuẩn nữa là **P1** — tạo authority thứ hai, vi phạm §3.

Nhưng toàn bộ máy móc đó được gắn vào **pull request**, và:

```
$ git log --merges --oneline | wc -l
0
$ git rev-list --count HEAD
91
```

**91 commit, 0 merge commit, tất cả đẩy thẳng lên `main`.** Nghĩa là:

| Thành phần | Đã chạy bao nhiêu lần |
| --- | --- |
| `.github/pull_request_template.md` (phân loại, evidence packet) | **0** |
| `.github/review-metadata.sh` (validate classification, evidence SHA, path-forced class) | **0** |
| `.github/CODEOWNERS` | **0** (không có branch protection thì file này không cản được ai) |
| `.github/BRANCH_RULESET.md` | **0** — và đây là spec cho một thiết lập hosting mà không file nào trong repo có thể cưỡng chế |

Không có thành phần nào sai. Chúng chỉ chưa bao giờ chạy — và theo §1 của chính
chuẩn: *"một quy tắc không thể tuân theo sẽ thối rữa, và kéo cả tài liệu theo nó"*.

---

## 1. Bốn lỗ hổng, theo thứ tự tác động

### 🔴 F1 (P1) — Quy trình được thiết kế cho PR; repo không dùng PR

Bằng chứng: bảng trên. Hệ quả cụ thể: rủi ro của một thay đổi **không được ghi ở đâu
cả**. §7 yêu cầu tác giả chọn đúng một class (Low/Normal/High/Architectural); commit
không có chỗ để ghi, nên thông tin đó tồn tại trong đầu người viết — hoặc của agent —
cho đến khi mất.

### 🔴 F2 (P1) — Quy trình review thực tế nằm ngoài repo, và đã stale

`CODE_REVIEW.md` §1 viết: *"This is already the operating rule in
`.pi/APPEND_SYSTEM.md` (auditor → implementer → auditor → test-auditor)."*

```
$ git check-ignore -v .pi/APPEND_SYSTEM.md
.gitignore:11:.pi/	.pi/APPEND_SYSTEM.md
```

Chuẩn review trích dẫn một file bị `.gitignore` loại trừ. **Đây chính xác là lớp lỗi
`refs/` từng làm `main` mất cả package** (xem `CODE_REVIEW.md` §10): một thứ được trích
dẫn nhưng không có trong repo. Clone sạch, CI, và mọi agent session mới bắt đầu từ
`main` đều không đọc được §1.

Tệ hơn, nội dung đó **đã lỗi thời**, nên chỉ un-ignore thôi chưa đủ:

- trỏ `go/PARITY_PLAN.md`, `go/PARITY.md` — path trước khi mọi thứ chuyển vào `src/`;
- *"TypeScript remains behavioral authority"* — mâu thuẫn ADR 0020 (Go engine).

Một authority stale còn nguy hiểm hơn một authority vắng mặt: nó được tuân theo.

### 🟡 F3 (P2) — 6/91 commit subject không phải Conventional Commits

```
34a6980  F2: route proxy cluster behind proxy.Service + ...
21a569c  F2: route a.Runtime.Profiles (9) through profile.Service, budget 26 -> 17
2731705  F2: route a.Pools (7) through pools.Service, budget 33 -> 26
5382582  F2: route a.Presets (3) through presets.Service, budget 36 -> 33
c38c8e7  engine: measure all four serving paths, cap concurrency, harden listeners
bb1ce7e  engine: ctx-first account reads, bindings self-lock, lease heartbeat, ring budget
```

6,6%. Bốn cái đầu nằm trong 15 commit gần nhất — **đây chính là "chất lượng không đều"
mà ta cảm nhận được**, vì nó dồn về phía gần đây chứ không phải tàn dư cũ.

§4 nói commit subject *là* changelog, và `make release-plan` parse Conventional Commits.
Một subject không parse được = một release note **bị rơi im lặng**. Không có gì báo lỗi.

### 🟡 F4 (P2) — Không có bất kỳ thi hành nào ở máy local

```
$ git config --get core.hooksPath
(none)          # và .git/hooks/ không có hook nào hoạt động
```

Gate duy nhất chạy là CI — tức là **sau khi** thay đổi đã lên `main`. Không có gì đứng
giữa "agent vừa sửa code" và "code đã ở trên nhánh chung".

---

## 2. Thiết kế: ba lớp, nêu rõ cái nào chạy khi nào

Nguyên tắc: đưa rule xuống lớp thấp nhất có thể thi hành được. Không thể có hai người
review (§1 đã thừa nhận), nên lớp thấp nhất phải là máy.

| Lớp | Khi | Cơ chế | Trạng thái |
| --- | --- | --- | --- |
| **L0 — commit** | mỗi `git commit` | `.githooks/commit-msg`: Conventional Commits + `Risk:` trailer + `Reviewed-By:` (High/Arch) + `ADR:` (Arch) | **MỚI — đã tạo, đã mutation-check** |
| **L0 — push** | mỗi `git push` | `.githooks/pre-push`: `make review` (fmt-check + vet + test + gates) | **MỚI — đã tạo** |
| **L1 — CI/PR** | khi có PR | giữ nguyên `ci.yml` + `review-metadata` + CODEOWNERS + ruleset | đã có, chưa từng chạy |
| **L2 — phán đoán** | High/Arch | vai trò agent (auditor → implementer → auditor → test-auditor) | có trong `.pi/`, **bị ignore + stale (F2)** |

Lý do đẩy phân loại rủi ro xuống commit trailer: khi không có PR, **commit là đơn vị
review duy nhất tồn tại**. `Risk:` trên commit làm được đúng việc §7 định nghĩa, và
trở thành dữ liệu có thể grep được — `git log --format='%h %s%n%(trailers:key=Risk)'`.

### Những gì hook **không** làm

- Không thay thế review phán đoán. Nó chỉ bắt ba lỗi cơ học: subject sai định dạng,
  thiếu phân loại rủi ro, và High/Arch tự duyệt.
- Không có exemption. `git commit --no-verify` / `git push --no-verify` là **P0 lên
  người làm**, theo đúng §5 ("nới lỏng gate là P0 của reviewer, không phải tác giả").

---

## 3. File đã tạo

| File | Việc làm |
| --- | --- |
| `.githooks/commit-msg` | Conventional Commits subject; đúng một `Risk:` trailer thuộc 4 class; `Reviewed-By:` bắt buộc với High/Arch; `ADR:` bắt buộc với Architectural. Merge commit được bỏ qua. |
| `.githooks/pre-push` | Chạy `make review`. Không escape hatch. Từ chối push nếu `make` không có trên PATH — *gate không chạy được ≠ gate xanh*. |
| `.githooks/install.sh` | `git config core.hooksPath .githooks`, chmod, cảnh báo nếu hook bị `.gitignore` nuốt. Idempotent. |
| `.githooks/test.sh` | Mutation suite, 17 case. |

### Bằng chứng mutation (§5: gate chưa từng thấy fail là gate chưa được chứng minh)

```
baseline                                  17 passed, 0 failed   exit 0
M-A  chấp nhận "Risk: Medium"             FAIL "Risk value outside the four classes"
M-B  nới regex conventional commit        3 FAIL
M-C  bỏ yêu cầu Reviewed-By               FAIL "Risk: High without Reviewed-By"
                                                 "Risk: Architectural without ADR"
khôi phục                                 17 passed, 0 failed   exit 0
```

Hai case đầu tiên của suite là **subject thật từ lịch sử repo** (`34a6980`, `c38c8e7`),
nên suite đồng thời là hồ sơ regression cho chính sự không đồng đều nó được viết để
chặn.

**Lưu ý phương pháp — đáng nhớ:** mutation đầu tiên tôi thử *không* làm suite đỏ. Lý do
không phải test vô dụng: hook có hai lớp grep độc lập cho `Risk:` (một để lấy giá trị,
một để đếm), nên nới một lớp không đủ để pass. Bẫy ở đây là **kết luận "test xanh ⇒
test tốt"**; thực ra là mutation sai. Đừng tin một mutation duy nhất.

---

## 4. Việc cần làm — theo thứ tự

**A. Erik chạy một lệnh (30 giây)**

```bash
.githooks/install.sh
```

Chưa chạy thì toàn bộ phần trên là tài liệu. Hook chưa cài = gate chưa chạy = đúng căn
bệnh đang chữa.

**B. Erik chốt hai quyết định (không thể đoán thay)**

1. **`pre-push` chạy `make review` có chấp nhận được không?** Baseline gần nhất đo
   **10 phút 02 giây** (`TEAM_QUALITY_BASELINE_2026-09-08.md`; ghi chú cũ từng đo 4
   phút — đã tăng gấp rưỡi, cần đo lại trước khi quyết định).
   Nếu không chịu được: tách ra — `fmt-check + vet + gates` xuống `pre-commit`, test ở
   `pre-push`. **Đừng xoá bước**; xoá bước là nới gate.
2. **Giữ direct-to-main hay chuyển sang PR thật?** Nếu chuyển: bật ruleset theo
   `.github/BRANCH_RULESET.md` (không ai có thể bật từ repo), và lúc đó PR template +
   `review-metadata.sh` mới bắt đầu có giá trị. Nếu giữ direct-to-main: commit trailer
   là cơ chế duy nhất, và F1 được đóng bằng L0 chứ không phải bằng PR.

**C. Commit đợt này (tôi chưa commit — working tree đang bẩn)**

`.githooks/` hiện là untracked (`?? .githooks/`), tức là đúng lỗi F2/F3 đang phán.
Cần một commit riêng: `.githooks/` (4 file, đã `chmod +x`).

Tôi **cố ý không** đụng vào `CODE_REVIEW.md` và `Makefile`: cả hai đang ` M` trong
working tree với sửa của session khác. Đụng vào sẽ sweep work của người khác — vi phạm
§4 "one logical change". Hai sửa đó để dành cho session sạch:

- `CODE_REVIEW.md` §4: thêm mục "commit phải có `Risk:` trailer".
- `Makefile`: target `hooks` gọi `.githooks/install.sh`; và `make paths` phải fail nếu
  một file trong `.githooks/` không được track (đúng pattern đã dùng cho 4 tài liệu
  authority và cho `.go` bị ignore).

**D. Xử lý F2 — quy trình agent (P1)**

Chọn một: (a) un-ignore và **sửa lại nội dung** `.pi/APPEND_SYSTEM.md` cho khớp thế giới
sau ADR 0020; hoặc (b) gọn hơn — đưa nguyên tắc 6 bước auditor→implementer→auditor→
test-auditor vào hẳn `CODE_REVIEW.md` §1 và xoá `.pi/` khỏi `.gitignore`. Khuyến nghị
**(b)**: một authority nằm trong chuẩn thì được tuân theo, còn một file bị ignore thì
không.

**E. Dọn F3 — 6 subject đã lỡ**

Không sửa lại lịch sử (rewrite `main` là P0). Ghi nhận: release note của 6 commit đó đã
mất. `make release-plan` nên được kiểm tra ở mỗi lần cắt release để phát hiện lớp này
sớm hơn.

---

## 5. Cách tự kiểm chứng

```bash
.githooks/test.sh                 # 17 passed, 0 failed
git config --get core.hooksPath   # phải là .githooks
printf 'F2: legacy subject\n' > /tmp/m && .githooks/commit-msg /tmp/m; echo "exit=$?"
                                  # phải là exit=1 — chính case 34a6980
```

Nếu lệnh cuối cho `exit=0`, gate đã chết và §1 áp dụng.

---

## 6. Điều tài liệu này không giải quyết

- **Chất lượng code** (F3 trong `TEAM_QUALITY_BASELINE_2026-09-08.md`: 65 hàm ≥ 70 dòng
  là phần nổi của vấn đề thiết kế, không phải review). Gate không sửa được thiết kế;
  nó chỉ bắt những gì máy đo được.
- **Review phán đoán thật sự.** Với một người + agent, L2 vẫn là con người đọc diff.
  L0/L1 chỉ đảm bảo rằng khi ta đọc, những lỗi cơ học đã bị loại bỏ — để thời gian
  phán đoán không bị phí vào chuyện format subject.
