#!/usr/bin/env python3
"""Render a board-pdf HTML file to a PDF with one page per <section class="board">.

  build_pdf.py DOC.html --out OUT.pdf [--previews DIR] [--dpi 96] [--width 1440] [--page-height 4000]
               [--pad 60] [--chrome PATH] [--budget 8000] [--title "..."]

How it works: headless Chrome prints the HTML on very tall pages (an @page rule is injected into a
temporary copy next to the source, so relative paths keep working), then PyMuPDF crops every page to
its content, copies the URI links back (show_pdf_page drops annotations), drops blank pages, sets the
metadata and writes PNG previews. The script ends with a check list: page sizes, link count, embedded
fonts and warnings (content cut at the page end, Thai text without a Thai font, fallback glyphs, and
[placeholders] of assets/template.html still in the text; other square brackets are left alone).
Requires: Google Chrome or Chromium, Python with PyMuPDF (`pip install pymupdf`).
"""
import argparse
import pathlib
import re
import shutil
import subprocess
import sys
from html import unescape

import fitz  # PyMuPDF

CHROME_CANDIDATES = [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/Applications/Chromium.app/Contents/MacOS/Chromium",
    "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
    "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge",
]
THAI = re.compile(r"[฀-๿]")


def find_chrome(explicit: str | None) -> str:
    for candidate in ([explicit] if explicit else CHROME_CANDIDATES):
        if candidate and (pathlib.Path(candidate).exists() or shutil.which(candidate)):
            return candidate
    sys.exit("Chrome/Chromium not found: pass --chrome PATH")


def render(html: pathlib.Path, raw_pdf: pathlib.Path, width: int, height: int, chrome: str, budget: int) -> None:
    source = html.read_text(encoding="utf-8")
    page_rule = f"<style>@page {{ size: {width}px {height}px; margin: 0; }}</style>"
    patched = source.replace("</head>", page_rule + "</head>", 1) if "</head>" in source else page_rule + source
    temp = html.with_name(f".{html.stem}.render.html")
    temp.write_text(patched, encoding="utf-8")
    try:
        raw_pdf.unlink(missing_ok=True)
        cmd = [chrome, "--headless=new", "--disable-gpu", "--no-pdf-header-footer",
               "--allow-file-access-from-files", f"--virtual-time-budget={budget}",
               f"--print-to-pdf={raw_pdf}", temp.as_uri()]
        done = subprocess.run(cmd, capture_output=True, text=True, timeout=300)
    finally:
        temp.unlink(missing_ok=True)
    if done.returncode != 0 or not raw_pdf.exists():
        sys.exit(f"Chrome failed ({done.returncode}): {done.stderr[-1200:]}")


def content_box(page: fitz.Page) -> tuple[float, bool]:
    """Lowest y of anything drawn on the page, ignoring the full-page background; and whether anything is drawn."""
    width, height = page.rect.width, page.rect.height
    ys = [b[3] for b in page.get_text("blocks") if b[4].strip()]
    ys += [info["bbox"][3] for info in page.get_image_info()]
    for drawing in page.get_drawings():
        r = drawing["rect"]
        if r.width >= width * 0.98 and r.height >= height * 0.9:
            continue
        ys.append(r.y1)
    return (max(ys), True) if ys else (0.0, False)


def template_placeholders() -> list[str]:
    """The [bracketed] placeholder texts of assets/template.html, so real brackets in content are not flagged."""
    template = pathlib.Path(__file__).resolve().parent.parent / "assets" / "template.html"
    if not template.exists():
        return []
    found = [unescape(p) for p in re.findall(r"\[([^\]<>\n]{2,80})\]", template.read_text(encoding="utf-8"))]
    return sorted(set(found), key=found.index)


def html_title(html: pathlib.Path) -> str:
    match = re.search(r"<title>(.*?)</title>", html.read_text(encoding="utf-8"), re.S | re.I)
    return re.sub(r"\s+", " ", match.group(1)).strip() if match else html.stem


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("html")
    parser.add_argument("--out", required=True)
    parser.add_argument("--previews", help="folder for PNG previews (default: <out stem>-preview next to the PDF)")
    parser.add_argument("--dpi", type=int, default=96)
    parser.add_argument("--width", type=int, default=1440, help="board width in CSS px (must match .board width)")
    parser.add_argument("--page-height", type=int, default=4000, help="tall print page in CSS px; raise it for long boards")
    parser.add_argument("--pad", type=float, default=60, help="points kept below the lowest content")
    parser.add_argument("--chrome")
    parser.add_argument("--budget", type=int, default=8000, help="Chrome virtual time budget in ms (fonts, images)")
    parser.add_argument("--title")
    parser.add_argument("--subject", default="")
    parser.add_argument("--author", default="")
    args = parser.parse_args()

    html = pathlib.Path(args.html).expanduser().resolve()
    out = pathlib.Path(args.out).expanduser().resolve()
    out.parent.mkdir(parents=True, exist_ok=True)
    raw = out.with_name(f".{out.stem}.raw.pdf")
    render(html, raw, args.width, args.page_height, find_chrome(args.chrome), args.budget)

    src = fitz.open(raw)
    doc = fitz.open()
    warnings: list[str] = []
    for index, page in enumerate(src):
        bottom, drawn = content_box(page)
        if not drawn:
            warnings.append(f"raw page {index + 1} is blank and was dropped (stray content after the last board?)")
            continue
        if bottom >= page.rect.height - 2:
            warnings.append(f"board {index + 1} reaches the end of the print page: raise --page-height (now {args.page_height}) or split the board")
        clip = fitz.Rect(0, 0, page.rect.width, min(page.rect.height, bottom + args.pad))
        new = doc.new_page(width=clip.width, height=clip.height)
        new.show_pdf_page(new.rect, src, index, clip=clip)
        for link in page.get_links():
            if link.get("kind") == fitz.LINK_URI and link["from"].y0 < clip.y1:
                new.insert_link({"kind": fitz.LINK_URI, "from": link["from"], "uri": link["uri"]})
    doc.set_metadata({"title": args.title or html_title(html), "subject": args.subject, "author": args.author})
    doc.save(out, garbage=4, deflate=True)
    src.close()
    raw.unlink(missing_ok=True)

    final = fitz.open(out)
    previews = pathlib.Path(args.previews).expanduser() if args.previews else out.with_name(f"{out.stem}-preview")
    previews.mkdir(parents=True, exist_ok=True)
    for old in previews.glob("page-*.png"):
        old.unlink()
    fonts: set[str] = set()
    text = ""
    for index, page in enumerate(final):
        page.get_pixmap(dpi=args.dpi).save(previews / f"page-{index + 1}.png")
        fonts.update(f[3].split("+", 1)[-1] for f in page.get_fonts(full=True) if f[3])
        text += page.get_text()
    links = sum(len(page.get_links()) for page in final)
    if THAI.search(text) and not any("Thai" in name for name in fonts):
        warnings.append("Thai text found but no Thai font embedded: check fonts.css paths")
    if any(name.startswith(("LastResort", ".LastResort")) for name in fonts):
        warnings.append("LastResort font embedded: some glyphs have no font (boxes in the PDF)")
    def squash(value: str) -> str:  # placeholders may wrap across lines or be uppercased by CSS (kickers)
        return re.sub(r"\s+", "", value).casefold()
    leftovers = [p for p in template_placeholders() if f"[{squash(p)}]" in squash(text)]
    if leftovers:
        shown = ", ".join(f"[{p}]" for p in leftovers[:3])
        warnings.append(f"{len(leftovers)} template placeholder(s) still in the PDF, for example {shown}: replace or remove them")

    print(f"PDF      {out}  ({out.stat().st_size / 1024 / 1024:.1f} MB, {len(final)} pages, {links} links)")
    print("pages    " + ", ".join(f"{p.rect.width:.0f}x{p.rect.height:.0f}" for p in final))
    print(f"fonts    {', '.join(sorted(fonts))}")
    print(f"previews {previews}/page-N.png  (open them and check every board before delivering)")
    for warning in warnings:
        print(f"WARNING  {warning}")


if __name__ == "__main__":
    main()
