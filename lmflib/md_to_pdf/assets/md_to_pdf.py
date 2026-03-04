#!/usr/bin/env python3
"""Convert Markdown to PDF via HTML+CSS using WeasyPrint.

Usage:
  md_to_pdf.py INPUT.md [OUTPUT.pdf] [--style STYLE.css]

If OUTPUT is omitted, uses INPUT stem + .pdf.
"""

import argparse
import sys
from pathlib import Path

import markdown
from weasyprint import HTML


def md_to_pdf(md_path: str, pdf_path: str | None = None, style_path: str | None = None):
    md_file = Path(md_path)
    if not md_file.exists():
        print(f"Error: {md_file} not found", file=sys.stderr)
        sys.exit(1)

    pdf_file = Path(pdf_path) if pdf_path else md_file.with_suffix(".pdf")

    css = ""
    if style_path:
        css_file = Path(style_path)
        if css_file.exists():
            css = css_file.read_text()

    md_text = md_file.read_text(encoding="utf-8")
    extensions = ["tables", "fenced_code", "codehilite", "toc", "nl2br"]
    html_body = markdown.markdown(md_text, extensions=extensions)

    html = f"""<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>{css}</style></head>
<body>{html_body}</body></html>"""

    HTML(string=html, base_url=str(md_file.parent)).write_pdf(str(pdf_file))
    print(pdf_file)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Convert Markdown to PDF")
    parser.add_argument("input", help="Input .md file")
    parser.add_argument("output", nargs="?", help="Output .pdf file (default: same name)")
    parser.add_argument("--style", help="Custom CSS stylesheet path")
    args = parser.parse_args()
    md_to_pdf(args.input, args.output, args.style)
