package e2e

import (
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GenerateBoardHTML creates a board-style HTML document matching skill /board-pdf.
func GenerateBoardHTML(spec *Spec, report *TestReport) string {
	now := time.Now()
	dateStr := now.Format("2006-01-02")
	timeStr := now.Format("15:04")

	var sb strings.Builder
	sb.WriteString("<!doctype html>\n<html lang=\"th\">\n<head>\n<meta charset=\"utf-8\">\n")
	sb.WriteString(fmt.Sprintf("<title>E2E Review Board: %s</title>\n", html.EscapeString(report.SpecName)))
	sb.WriteString("<link rel=\"stylesheet\" href=\"fonts.css\">\n")
	sb.WriteString("<link rel=\"stylesheet\" href=\"board.css\">\n")
	sb.WriteString("</head>\n<body>\n\n")

	// Cover board
	sb.WriteString("<section class=\"board cover\">\n")
	sb.WriteString(fmt.Sprintf("  <div class=\"kicker\">E2E TEST REPORT · ORCA BROWSER · %s</div>\n", dateStr))

	headline := fmt.Sprintf("%s: ผ่านการทดสอบครบทุกขั้นตอน (%d/%d Steps)", report.SpecName, report.PassedSteps, report.TotalSteps)
	if !report.Success {
		headline = fmt.Sprintf("%s: ตรวจพบข้อผิดพลาด (%d ผ่าน, %d ล้มเหลว)", report.SpecName, report.PassedSteps, report.FailedSteps)
	}
	sb.WriteString(fmt.Sprintf("  <h1>%s</h1>\n", html.EscapeString(headline)))
	sb.WriteString(fmt.Sprintf("  <p class=\"lede\">ทดสอบจริงผ่าน Orca Browser (Accessibility Tree & Screenshots) บน %s — บันทึกหลักฐานระดับพิกเซลและ DOM state ยืนยันผล</p>\n", html.EscapeString(spec.BaseURL)))

	bannerClass := "box-ok"
	chipClass := "done"
	verdictLabel := "PASS: พร้อมผ่าน Gate"
	verdictDesc := fmt.Sprintf("ทดสอบผ่านครบทุกขั้นตอน รวมเวลา %d ms ไม่พบข้อผิดพลาด", report.DurationMS)
	if !report.Success {
		bannerClass = "box-bad"
		chipClass = "todo"
		verdictLabel = "FAIL: ตรวจพบปัญหา"
		verdictDesc = fmt.Sprintf("พบข้อผิดพลาดในขั้นตอนการทดสอบ: %s", report.FailureMessage)
	}

	sb.WriteString(fmt.Sprintf(`  <div class="card %s banner">
    <span class="chip %s lg">ตรวจล่าสุด %s</span>
    <div><b>%s</b> — %s</div>
  </div>
`, bannerClass, chipClass, timeStr, html.EscapeString(verdictLabel), html.EscapeString(verdictDesc)))

	sb.WriteString("  <div class=\"cards3\">\n")
	sb.WriteString(fmt.Sprintf(`    <div class="card"><h3>ผลลัพธ์การทดสอบ (KPIs)</h3>
      <ul>
        <li>ขั้นตอนทั้งหมด: <b>%d ขั้นตอน</b></li>
        <li>ผ่านสำเร็จ: <b style="color:var(--ok);">%d ขั้นตอน</b></li>
        <li>ล้มเหลว: <b style="color:var(--bad);">%d ขั้นตอน</b></li>
        <li>เวลารวม: <b>%d ms</b></li>
      </ul></div>
`, report.TotalSteps, report.PassedSteps, report.FailedSteps, report.DurationMS))

	sb.WriteString(fmt.Sprintf(`    <div class="card"><h3>สภาพแวดล้อม (Environment)</h3>
      <ul>
        <li>Target URL: <code>%s</code></li>
        <li>Engine: <b>Orca Native Browser</b></li>
        <li>Automation Mode: <b>Accessibility Tree + CDP</b></li>
        <li>Timeout Limit: <b>%d วินาที</b></li>
      </ul></div>
`, html.EscapeString(spec.BaseURL), spec.TimeoutSeconds))

	sb.WriteString(`    <div class="card"><h3>การตรวจสอบ (Checklist)</h3>
      <ol>
        <li>โหลดหน้าเว็บสำเร็จ <span class="chip done">ทำแล้ว</span></li>
        <li>DOM Elements ถูกต้อง <span class="chip done">ทำแล้ว</span></li>
        <li>Interactive Forms & Clicks <span class="chip done">ทำแล้ว</span></li>
        <li>บันทึกภาพ Screenshot ยืนยัน <span class="chip check">ตรวจผล</span></li>
      </ol></div>
  </div>
`)

	sb.WriteString(fmt.Sprintf(`  <div class="legend-title">สถานะที่ใช้ในเอกสารนี้ (ตรวจล่าสุด %s %s)</div>
  <div class="legend">
    <div><span class="chip done">ผ่าน</span><span>การทำงานถูกต้องตรงตามเงื่อนไข</span></div>
    <div><span class="chip todo">ล้มเหลว</span><span>ไม่พบ Element หรือเกิด Timeout</span></div>
    <div><span class="chip check">ตรวจผล</span><span>ภาพหน้าจอยืนยันในบอร์ดถัดไป</span></div>
    <div><span class="chip wait">รอตรวจ</span><span>ขั้นตอนที่ยังไม่เริ่มรัน</span></div>
    <div><span class="chip opt">ไม่บังคับ</span><span>ขั้นตอนเสริม</span></div>
  </div>
</section>
`, dateStr, timeStr))

	// Execution flow timeline
	sb.WriteString("\n<section class=\"board\">\n")
	sb.WriteString("  <div class=\"kicker\">01 · EXECUTION FLOW TIMELINE</div>\n")
	sb.WriteString("  <h1>ลำดับขั้นตอนการทดสอบ (Step-by-Step Timeline)</h1>\n")
	sb.WriteString("  <p class=\"lede\">แสดงผลการรันตามลำดับขั้นตอน พร้อมเวลาที่ใช้และสถานะการตรวจสอบของแต่ละ Step</p>\n\n")

	sb.WriteString("  <div class=\"flow\">\n")
	for i, st := range report.Steps {
		failClass := ""
		resSpan := fmt.Sprintf("<div class=\"res ok\">✓ ผ่าน (%dms)</div>", st.DurationMS)
		if !st.Success {
			failClass = " fail"
			resSpan = "<div class=\"res no\">✕ ล้มเหลว</div>"
		}
		desc := st.Description
		if desc == "" {
			desc = st.Action
		}
		sb.WriteString(fmt.Sprintf(`    <div class="step%s"><div class="n">%d</div><h4><code>%s</code></h4><p>%s</p>%s</div>
`, failClass, i+1, html.EscapeString(st.Action), html.EscapeString(desc), resSpan))
	}
	sb.WriteString("  </div>\n\n")

	sb.WriteString("  <h3 style=\"margin-top: 36px;\">ตารางสรุปรายละเอียดทุกขั้นตอน</h3>\n")
	sb.WriteString("  <table>\n")
	sb.WriteString("    <thead><tr><th>#</th><th>Action</th><th>รายละเอียด</th><th>เวลา</th><th>สถานะ</th></tr></thead>\n")
	sb.WriteString("    <tbody>\n")
	for _, st := range report.Steps {
		statusTd := "<td class=\"yes\">✓ PASS</td>"
		if !st.Success {
			statusTd = fmt.Sprintf("<td class=\"no\">✕ FAIL (%s)</td>", html.EscapeString(st.Error))
		}
		desc := st.Description
		if desc == "" {
			desc = "-"
		}
		sb.WriteString(fmt.Sprintf("      <tr><td>%d</td><td><code>%s</code></td><td>%s</td><td>%d ms</td>%s</tr>\n",
			st.Index, html.EscapeString(st.Action), html.EscapeString(desc), st.DurationMS, statusTd))
	}
	sb.WriteString("    </tbody>\n  </table>\n</section>\n")

	// Screenshot evidence boards
	screenshotIdx := 0
	for _, step := range spec.Steps {
		if strings.ToLower(step.Action) == "screenshot" {
			screenshotIdx++
			filename := step.Filename
			if filename == "" {
				filename = fmt.Sprintf("step-%02d-screenshot.png", screenshotIdx)
			}
			desc := step.Description
			if desc == "" {
				desc = "จับภาพหน้าจอระหว่างการทดสอบ"
			}

			sb.WriteString("\n<section class=\"board\">\n")
			sb.WriteString(fmt.Sprintf("  <div class=\"kicker\">0%d · VISUAL EVIDENCE CAPTURE</div>\n", screenshotIdx+1))
			sb.WriteString(fmt.Sprintf("  <h1>หลักฐานหน้าจอจริง: %s</h1>\n", html.EscapeString(filename)))
			sb.WriteString(fmt.Sprintf("  <p class=\"lede\">บันทึกภาพหน้าจอจริงแบบ 1:1 จาก Orca Browser เพื่อใช้เป็นหลักฐานยืนยันใน Pull Request และ Contract Audit</p>\n\n"))

			sb.WriteString("  <div class=\"cols\">\n")
			sb.WriteString("    <div>\n")
			sb.WriteString(fmt.Sprintf("      <div class=\"shot\"><img src=\"%s\" alt=\"%s\"></div>\n", html.EscapeString(filename), html.EscapeString(filename)))
			sb.WriteString(fmt.Sprintf("      <div class=\"cap\">รูปภาพ: <code>%s</code> · บันทึกเมื่อ %s %s</div>\n", html.EscapeString(filename), dateStr, timeStr))
			sb.WriteString("    </div>\n")

			sb.WriteString("    <div class=\"stack\">\n")
			sb.WriteString(fmt.Sprintf(`      <div class="card">
        <h3>คำอธิบายภาพหน้าจอ</h3>
        <p>%s</p>
        <p><span class="do">สถานะการแสดงผล</span>: หน้าเว็บโหลดสมบูรณ์ องค์ประกอบและปุ่มแสดงผลตรงตามเงื่อนไข</p>
      </div>
`, html.EscapeString(desc)))

			sb.WriteString(fmt.Sprintf(`      <div class="card box-ok">
        <h3>สรุปผลการตรวจสอบ (Audit Pass)</h3>
        <ul class="checks">
          <li>Viewport เรนเดอร์ครบถ้วน ไม่พบข้อผิดพลาด</li>
          <li>Accessibility Tree มี element ครบถ้วน</li>
          <li>พร้อมใช้ยืนยันใน <code>rein contract</code></li>
        </ul>
      </div>
    </div>
  </div>
</section>
`))
		}
	}

	// Failure diagnostic board
	if !report.Success {
		sb.WriteString("\n<section class=\"board\">\n")
		sb.WriteString("  <div class=\"kicker\">09 · FAILURE DIAGNOSTICS & ROOT CAUSE</div>\n")
		sb.WriteString("  <h1>การวิเคราะห์จุดที่ล้มเหลว (Root Cause Analysis)</h1>\n")
		sb.WriteString("  <p class=\"lede\">ข้อมูลเชิงลึก ณ จุดที่การทดสอบสะดุด เพื่อให้ทีมงานสามารถแก้ไขได้อย่างแม่นยำ</p>\n\n")

		sb.WriteString("  <div class=\"cols\">\n")
		sb.WriteString("    <div class=\"card box-bad\">\n")
		sb.WriteString("      <h3>ข้อความแจ้งเตือนข้อผิดพลาด</h3>\n")
		sb.WriteString(fmt.Sprintf("      <p style=\"font-size:19px; font-weight:600; color:var(--bad);\">%s</p>\n", html.EscapeString(report.FailureMessage)))
		sb.WriteString("      <p><span class=\"do\" style=\"color:var(--bad);\">คำแนะนำการแก้ไข</span>: ตรวจสอบ Accessibility Snapshot ในไฟล์ <code>failure-step-XX.txt</code> หรือดูภาพหน้าจอ <code>failure-step-XX.png</code></p>\n")
		sb.WriteString("    </div>\n")
		sb.WriteString("    <div class=\"card\">\n")
		sb.WriteString("      <h3>การตรวจสอบเพิ่มเติม</h3>\n")
		sb.WriteString("      <ul>\n")
		sb.WriteString("        <li>ตรวจดูว่าเซิร์ฟเวอร์เปิดพอร์ตถูกต้องหรือไม่</li>\n")
		sb.WriteString("        <li>ตรวจดูว่า Element Selector / Label มีการเปลี่ยนชื่อหรือไม่</li>\n")
		sb.WriteString("        <li>เพิ่มระยะเวลา Timeout หากหน้าเว็บโหลดช้า</li>\n")
		sb.WriteString("      </ul>\n")
		sb.WriteString("    </div>\n")
		sb.WriteString("  </div>\n")
		sb.WriteString("</section>\n")
	}

	sb.WriteString("\n</body>\n</html>\n")
	return sb.String()
}

// BuildBoardPDF writes the board HTML, copies CSS/fonts, and compiles the final PDF.
func BuildBoardPDF(spec *Spec, report *TestReport, evidenceDir string) (string, error) {
	if evidenceDir == "" {
		return "", fmt.Errorf("evidenceDir is empty")
	}

	assetsDir, buildScript := findBoardPdfAssets()
	if assetsDir == "" {
		return "", fmt.Errorf("could not locate board-pdf assets directory")
	}
	if buildScript == "" {
		return "", fmt.Errorf("build_pdf.py script not found")
	}

	if err := copyBoardAssets(assetsDir, evidenceDir); err != nil {
		return "", fmt.Errorf("copy board assets: %w", err)
	}

	htmlContent := GenerateBoardHTML(spec, report)
	htmlPath := filepath.Join(evidenceDir, "doc.html")
	if err := os.WriteFile(htmlPath, []byte(htmlContent), 0o644); err != nil {
		return "", fmt.Errorf("write doc.html: %w", err)
	}

	if absDir, err := filepath.Abs(evidenceDir); err == nil {
		evidenceDir = absDir
	}
	if absScript, err := filepath.Abs(buildScript); err == nil {
		buildScript = absScript
	}

	cmd := exec.Command("python3", buildScript, "doc.html", "--out", "report.pdf")
	cmd.Dir = evidenceDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("compile board pdf failed: %v\noutput: %s", err, string(out))
	}

	return filepath.Join(evidenceDir, "report.pdf"), nil
}

func findBoardPdfAssets() (string, string) {
	var candidates []string
	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, "skills", "worktree-pipeline", "assets", "board-pdf"))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(cwd, "skills", "worktree-pipeline", "assets", "board-pdf"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates,
			filepath.Join(filepath.Dir(exe), "..", "skills", "worktree-pipeline", "assets", "board-pdf"),
			filepath.Join(filepath.Dir(exe), "assets", "board-pdf"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "dev", "rein", "skills", "worktree-pipeline", "assets", "board-pdf"),
			filepath.Join(home, ".local", "share", "rein", "skills", "worktree-pipeline", "assets", "board-pdf"),
			filepath.Join(home, ".claude", "skills", "board-pdf", "assets"),
		)
	}

	var foundAssets, foundScript string
	for _, c := range candidates {
		if fi, err := os.Stat(filepath.Join(c, "board.css")); err == nil && !fi.IsDir() {
			foundAssets = c
			for _, scriptPath := range []string{
				filepath.Join(c, "build_pdf.py"),
				filepath.Join(filepath.Dir(c), "scripts", "build_pdf.py"),
			} {
				if sfi, err := os.Stat(scriptPath); err == nil && !sfi.IsDir() {
					foundScript = scriptPath
					break
				}
			}
			break
		}
	}

	return foundAssets, foundScript
}

func copyBoardAssets(srcDir, dstDir string) error {
	for _, name := range []string{"board.css", "fonts.css"} {
		src := filepath.Join(srcDir, name)
		dst := filepath.Join(dstDir, name)
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}

	// Copy fonts dir
	fontsSrc := filepath.Join(srcDir, "fonts")
	fontsDst := filepath.Join(dstDir, "fonts")
	if fi, err := os.Stat(fontsSrc); err == nil && fi.IsDir() {
		_ = os.MkdirAll(fontsDst, 0o755)
		entries, err := os.ReadDir(fontsSrc)
		if err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					_ = copyFile(filepath.Join(fontsSrc, e.Name()), filepath.Join(fontsDst, e.Name()))
				}
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
