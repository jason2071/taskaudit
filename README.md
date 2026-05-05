# taskaudit

CLI tool สำหรับ audit code ของ Go project เทียบกับ checklist โดยใช้ Claude API

## Quick Start

```bash
# 1. Build
go build -o taskaudit

# 2. Set API key
export ANTHROPIC_API_KEY="sk-ant-..."

# 3. Run (ใช้ default checklist)
taskaudit -task "ชื่องาน" -dir ./path-to-project
```

แค่นี้ก็ได้ผลลัพธ์ audit report บน terminal แล้ว

## Install

```bash
go build -o taskaudit
sudo mv taskaudit /usr/local/bin/   # หรือเก็บไว้ใน path ที่ใช้สะดวก
```

## Usage

### ใช้ default checklist (เร็วสุด)

```bash
taskaudit -task "Planogram compare API" -dir ./planogram-service
```

tool จะใช้ checklist มาตรฐาน (model, repository, service, handler, validation, error handling, unit test, docs) ตรวจให้อัตโนมัติ

### ใช้ custom checklist

สร้างไฟล์ `checklist.txt` (format: `category: title` ต่อบรรทัด):

```
code: สร้าง model
code: สร้าง repository
code: สร้าง service พร้อม business logic
code: สร้าง handler + routing
code: เพิ่ม validation
test: เขียน unit test สำหรับ service
test: เขียน integration test
docs: เขียน comment สำคัญ
```

```bash
taskaudit -task "ชื่องาน" -checklist ./checklist.txt -dir ./my-service
```

### Export report

```bash
# HTML (เปิด browser อัตโนมัติ)
taskaudit -task "..." -dir ./my-service -html ./audit.html -open

# Markdown
taskaudit -task "..." -dir ./my-service -md ./audit.md

# JSON (pipe ต่อได้)
taskaudit -task "..." -json | jq '.missingItems'
```

### Custom scan paths

ถ้า project layout ไม่ standard:

```bash
taskaudit -task "..." -include "app/handlers,app/services,app/repos"
```

## Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-task` | (required) | ชื่องาน |
| `-desc` | "" | รายละเอียดงาน |
| `-dir` | `.` | Root directory |
| `-checklist` | (use default) | path ไฟล์ checklist |
| `-include` | `internal/handler,internal/service,internal/repository,internal/models` | Paths ที่จะสแกน |
| `-tests` | `true` | รวม `_test.go` files |
| `-json` | `false` | Output เป็น JSON |
| `-html` | "" | Export HTML report ไปที่ path (e.g. `./audit.html`) |
| `-md` | "" | Export Markdown report ไปที่ path (e.g. `./audit.md`) |
| `-open` | `false` | เปิด HTML report ใน browser (ใช้คู่กับ `-html`) |
| `-v` | `false` | Verbose |

## Example Output

```
═══ CODE AUDIT REPORT ═══

📊 Summary:
   Code มี handler/service/repository ครบตาม clean architecture แต่
   ยังขาด unit test ทั้งหมดและ validation ที่ DTO ระดับ handler

📈 Stats:
   ● done: 5   ● missing: 3   ● partial: 1   ● n/a: 2

✓ Checklist Results:
   ✓ สร้าง model (internal/models)
     เห็น PlanogramCompare struct ใน planogram.go
   ✓ สร้าง repository layer
     CompareRepo มี method GetByDate, BulkInsert
   ◐ สร้าง handler + routing
     มี handler แต่ไม่มี route registration
   ✗ เขียน unit test (table-driven)
     ไม่พบไฟล์ *_test.go ใน service layer

⚠ สิ่งที่ขาดเพิ่มเติม (ไม่อยู่ใน checklist):
   [HIGH] เพิ่ม transaction handling สำหรับ bulk insert
     planogram_repo.go ใช้ multiple INSERT แยก ควรห่อด้วย tx
   [MEDIUM] Logger context ขาด trace ID
     ทำให้ debug ใน production ยาก
```

## Alias (แนะนำ)

ตั้ง alias ใน shell profile (`.zshrc` / `.bashrc`) ให้ใช้สั้นลง:

```bash
# ปรับ -include ตาม project layout ของตัวเอง
alias audit-task='taskaudit -include "handler,service,repository,model,utils"'
```

ใช้งาน:

```bash
audit-task -task "User management API" -dir ./user-service
audit-task -task "Payment flow" -dir ./payment -html ./report.html -open
```

## Tips

- ลองรัน `-v` ดูว่ามัน scan ไฟล์ไหนไปบ้าง ก่อนปรับ `-include`
- ถ้า project ใหญ่มาก (50+ files) แนะนำใช้ `-include` ระบุ folder เฉพาะที่เกี่ยวกับงาน
- รวมเข้า git pre-push hook ได้ — บล็อกการ push ถ้า audit ขึ้น MISSING ระดับ HIGH
