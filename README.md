# taskaudit

ตรวจ code ของ Go project ว่าทำครบตาม checklist หรือยัง — ใช้ Claude AI วิเคราะห์ให้

## เริ่มใช้งาน

```bash
# Build + install
go build -o taskaudit
sudo mv taskaudit /usr/local/bin/

# ตั้ง API key (ใส่ใน .zshrc ก็ได้)
export ANTHROPIC_API_KEY="sk-ant-..."
```

## Quick Start

### กรณี project ใช้ `internal/` (Go standard layout)

```
my-project/
├── internal/
│   ├── handler/
│   ├── service/
│   ├── repository/
│   ├── models/
│   └── middleware/
└── main.go
```

ไม่ต้องใส่ `-include` — scan `internal/*` อัตโนมัติ:

```bash
taskaudit -task "User CRUD API" -dir ./my-project
```

### กรณี project ไม่ใช้ `internal/` (flat layout)

```
my-project/
├── handler/
├── service/
├── repository/
├── model/
├── utils/
├── config/
└── main.go
```

ต้องระบุ `-include`:

```bash
taskaudit -task "User CRUD API" -include "handler,service,repository,model,utils"
```

### อยู่ใน project แล้ว — ไม่ต้องใส่ `-dir`

```bash
cd ~/projects/my-api
taskaudit -task "User CRUD API" -include "handler,service,repository,model,utils"
```

## Output Formats

```bash
# Terminal (default)
taskaudit -task "ชื่องาน" -include "handler,service,repository,model"

# HTML — เปิดใน browser เลย
taskaudit -task "ชื่องาน" -html ./report.html -open

# Markdown — เอาไปแปะ PR / Notion
taskaudit -task "ชื่องาน" -md ./report.md

# JSON — pipe ต่อได้
taskaudit -task "ชื่องาน" -json | jq '.missingItems'

# สร้างหลาย format พร้อมกัน
taskaudit -task "Payment Integration" \
  -include "handler,service,repository,model,utils" \
  -html ./audit.html \
  -md ./audit.md
```

## Custom Checklist

สร้างไฟล์ checklist ติด project (format: `category: title`):

```
code: สร้าง model ใน model/ folder พร้อม field validation tag
code: สร้าง repository ใน repository/ พร้อม raw SQL ที่เหมาะสม
code: สร้าง service ใน service/ พร้อม interface + impl + validation
code: error mapping ผ่าน utils/errors_response.go (HandleError)
code: ใช้ sentinel errors จาก utils/errors.go
code: สร้าง handler ใน handler/ + register route ใน main.go
test: เขียน unit test table-driven สำหรับ service layer
test: test repository ด้วย sqlmock หรือ test database
docs: comment business logic ที่ซับซ้อน
```

รัน:

```bash
taskaudit \
  -task "Planogram compare API" \
  -desc "เปรียบเทียบ planogram_scm_data กับ pdf_files" \
  -checklist ./audit-checklist.txt \
  -include "handler,service,repository,model,utils" \
  -v
```

## ตั้ง Alias ให้สั้นลง

```bash
# ใส่ใน .zshrc
alias audit-task='taskaudit -include "handler,service,repository,model,utils"'
```

ใช้:

```bash
audit-task -task "Planogram compare" -v
audit-task -task "User API" -dir ./user-service
audit-task -task "Payment" -dir ./payment -html ./report.html -open
```

## ตัวอย่างการใช้งานจริง

### ใส่ description ช่วยให้ AI เข้าใจ context

```bash
taskaudit -task "Planogram Compare" \
  -desc "API สำหรับเทียบ planogram ระหว่าง master กับ actual โดยรับ store_id + date แล้ว return diff" \
  -include "handler,service,repository,model" \
  -html ./report.html -open
```

### Audit เฉพาะ code (ไม่รวม test files)

```bash
taskaudit -task "Auth Middleware" \
  -include "middleware,handler,utils" \
  -tests=false \
  -v
```

### Project ขนาดเล็ก — scan ทั้งหมด

```bash
taskaudit -task "test audit" -include "." -v
```

### ใช้ใน CI/CD — ตรวจก่อน merge

```bash
# ใน GitHub Actions / GitLab CI
taskaudit -task "$PR_TITLE" \
  -dir . \
  -include "handler,service,repository,model" \
  -json | jq -e '.stats.missing == 0'
# exit code 1 ถ้ายังมี missing items → block merge
```

### Pipe JSON ไปวิเคราะห์ต่อ

```bash
# ดูเฉพาะ items ที่ยังไม่ได้ทำ
taskaudit -task "Search Feature" -include "handler,service" -json \
  | jq '.items[] | select(.status == "missing") | .title'

# นับจำนวน done vs missing
taskaudit -task "Notification" -include "handler,service" -json \
  | jq '.stats'
```

## ตัวอย่าง Output

```
═══ CODE AUDIT REPORT ═══

📊 Summary:
   Code มี handler/service/repository ครบ แต่ยังขาด unit test และ validation

📈 Stats:
   ● done: 5   ● missing: 3   ● partial: 1   ● n/a: 2

✓ Checklist Results:
   ✓ สร้าง model — เห็น PlanogramCompare struct
   ✓ สร้าง repository — CompareRepo มี GetByDate, BulkInsert
   ◐ สร้าง handler — มี handler แต่ไม่มี route registration
   ✗ เขียน unit test — ไม่พบ *_test.go ใน service layer

⚠ สิ่งที่ขาดเพิ่มเติม:
   [HIGH] เพิ่ม transaction handling สำหรับ bulk insert
   [MEDIUM] Logger context ขาด trace ID
```

## Flags ทั้งหมด

| Flag | Default | คำอธิบาย |
|------|---------|----------|
| `-task` | **(ต้องใส่)** | ชื่องาน |
| `-desc` | - | รายละเอียดเพิ่มเติมให้ AI เข้าใจ context |
| `-dir` | `.` | Root ของ project |
| `-checklist` | built-in | ไฟล์ checklist ที่จะใช้ |
| `-include` | `internal/*` | Folders ที่จะ scan (คั่นด้วย `,`) |
| `-tests` | `true` | รวม `_test.go` ด้วย |
| `-json` | `false` | Output เป็น JSON |
| `-html` | - | Export HTML ไปที่ path |
| `-md` | - | Export Markdown ไปที่ path |
| `-open` | `false` | เปิด HTML ใน browser (ใช้คู่ `-html`) |
| `-v` | `false` | แสดงรายละเอียดการ scan |

## Tips

- ใช้ `-v` ดูว่า scan ไฟล์ไหนบ้าง ก่อนปรับ `-include`
- `-dir` default คือ `.` → cd เข้า project แล้วไม่ต้องใส่
- Project ใหญ่ (50+ files) → ระบุ `-include` เฉพาะ folder ที่เกี่ยว
- ใส่ใน git pre-push hook ได้ — บล็อก push ถ้ามี MISSING ระดับ HIGH
- `-desc` ยิ่งละเอียด AI วิเคราะห์ได้แม่นยำขึ้น
