# taskaudit

ตรวจ code ของ Go project ว่าทำครบตาม checklist หรือยัง — ใช้ Claude AI วิเคราะห์ให้

## เริ่มใช้งาน

```bash
# Build
go build -o taskaudit
sudo mv taskaudit /usr/local/bin/

# ตั้ง API key (ใส่ใน .zshrc ก็ได้)
export ANTHROPIC_API_KEY="sk-ant-..."
```

## วิธีใช้

### แบบง่ายสุด — ชี้ไปที่ project แล้วบอกชื่องาน

```bash
taskaudit -task "ชื่องาน" -dir ./my-project
```

มันจะ scan code ใน `internal/` แล้วเทียบกับ checklist มาตรฐาน (model, repo, service, handler, validation, test, docs)

### อยากกำหนด checklist เอง

สร้างไฟล์ `checklist.txt`:

```
code: สร้าง model
code: สร้าง repository
code: สร้าง service
code: สร้าง handler + routing
test: เขียน unit test
test: เขียน integration test
```

แล้วรัน:

```bash
taskaudit -task "ชื่องาน" -checklist ./checklist.txt -dir ./my-project
```

### อยากได้ report สวยๆ

```bash
# HTML — เปิดใน browser เลย
taskaudit -task "ชื่องาน" -dir ./my-project -html ./report.html -open

# Markdown — เอาไปแปะ PR / Notion
taskaudit -task "ชื่องาน" -dir ./my-project -md ./report.md

# JSON — pipe ต่อได้
taskaudit -task "ชื่องาน" -dir ./my-project -json | jq '.missingItems'
```

### Project layout ไม่ standard

บอกว่าจะ scan folder ไหน:

```bash
taskaudit -task "ชื่องาน" -include "handler,service,repository,model,utils" -dir ./my-project
```

## ตั้ง Alias ให้ใช้สั้นลง

```bash
# ใส่ใน .zshrc
alias audit-task='taskaudit -include "handler,service,repository,model,utils"'
```

แล้วใช้:

```bash
audit-task -task "User API" -dir ./user-service
audit-task -task "Payment" -dir ./payment -html ./report.html -open
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
| `-desc` | - | รายละเอียดเพิ่มเติม |
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
- Project ใหญ่ (50+ files) → ระบุ `-include` เฉพาะ folder ที่เกี่ยว
- ใส่ใน git pre-push hook ได้ — บล็อก push ถ้ามี MISSING ระดับ HIGH
