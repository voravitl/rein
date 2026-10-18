#!/usr/bin/env python3
"""thai-scan — จับจุดตัวอักษรเพี้ยนในร่างข้อความไทย (ใช้กับ skill thai-communication)

จับได้:
  1. อักษรต่างชุด (จีน ญี่ปุ่น เกาหลี รัสเซีย) ที่แทรกในข้อความ
  2. ไทยกับอังกฤษติดกันกลางคำ โดยไม่มีตัวคั่น (จุดเสียแบบ "หรุbrates")
  3. สระ/วรรณยุกต์ซ้อนเกิน 2 ตัวติดกัน
  4. อักษรล่องหน หรือ replacement char

จับไม่ได้ (โดยธรรมชาติ):
  - คำไทยที่ถูกหลักตัวอักษรแต่เลือกคำผิด เช่น โหด/โกด
  - ประโยคที่อ่านไม่รู้เรื่องแต่สะกดถูก
  กันด้วย: ประโยคสั้น + คำไทยที่คุ้นเคย + อ่านทวนก่อนส่ง

ใช้: python3 thai-scan.py <ไฟล์ร่าง>
คืนค่า: 0 = สะอาด, 1 = พบจุดต้องแก้
"""
import re
import sys
import unicodedata

THAI = re.compile(r'[\u0E01-\u0E5B]')
LATIN = re.compile(r'[A-Za-z]')
FOREIGN = re.compile(r'[\u3000-\u30FF\u3400-\u4DBF\u4E00-\u9FFF\uAC00-\uD7AF\u0400-\u04FF]')
COMBINING = re.compile(r'[\u0E31\u0E34-\u0E3A\u0E47-\u0E4E]')
INVISIBLE = re.compile(r'[\uFFFD\u200B\uFEFF\u200E\u200F]')


def context(line: str, start: int, end: int, pad: int = 15) -> str:
    return line[max(0, start - pad):end + pad].strip()


def main(path: str) -> int:
    with open(path, encoding="utf-8") as handle:
        text = handle.read()

    issues = 0
    for line_number, line in enumerate(text.splitlines(), start=1):
        for match in FOREIGN.finditer(line):
            char = match.group(0)
            name = unicodedata.name(char, "unknown")
            print(f"{path}:{line_number}: foreign-script [{name}] ... {context(line, match.start(), match.end())}")
            issues += 1

        for match in re.finditer(r'[\u0E01-\u0E5B][A-Za-z]|[A-Za-z][\u0E01-\u0E5B]', line):
            print(f"{path}:{line_number}: thai-latin-glued ... {context(line, match.start(), match.end())}")
            issues += 1

        for match in re.finditer(r'[\u0E31\u0E34-\u0E3A\u0E47-\u0E4E]{3,}', line):
            print(f"{path}:{line_number}: combining-stack ... {context(line, match.start(), match.end())}")
            issues += 1

        for match in INVISIBLE.finditer(line):
            print(f"{path}:{line_number}: invisible-or-replacement char at column {match.start() + 1}")
            issues += 1

    if issues:
        print(f"-- {issues} issue(s) found, fix before sending")
        return 1
    print("-- clean")
    return 0


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print("usage: python3 thai-scan.py <draft-file>")
        sys.exit(2)
    sys.exit(main(sys.argv[1]))
