#!/usr/bin/env python3
"""Export the PNG files and favicon.ico from the SVG sources in assets/brand/.

Requirements: Google Chrome (headless) and Pillow. Run from the repo root:

    python3 hack/brand-export.py

Chrome draws each SVG at the true output size, so the 16 px file shows
what a browser shows at 16 px.
"""
import os
import subprocess
import sys
import tempfile

from PIL import Image

BRAND = "assets/brand"
SIZES = [16, 32, 64, 128, 256, 512]
CHROME = os.environ.get("CHROME", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")


def render(svg, width, height, out, tmp):
    """Write a PNG of svg at width x height with a transparent background."""
    html = os.path.join(tmp, "page.html")
    with open(html, "w") as f:
        f.write(f'<html><body style="margin:0"><img src="file://{os.path.abspath(svg)}" '
                f'style="display:block;width:{width}px;height:{height}px"></body></html>')
    shot = os.path.join(tmp, "shot.png")
    subprocess.run([CHROME, "--headless=new", f"--user-data-dir={tmp}/profile", "--no-first-run",
                    "--disable-gpu", "--hide-scrollbars", "--force-device-scale-factor=1",
                    "--allow-file-access-from-files", "--default-background-color=00000000",
                    f"--window-size={max(width, 320)},{max(height, 320)}", f"--screenshot={shot}",
                    f"file://{html}"], check=True, capture_output=True, timeout=120)
    Image.open(shot).crop((0, 0, width, height)).save(out)


def main():
    os.makedirs(f"{BRAND}/png", exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        for name in ("koodam-mark", "koodam-mark-dark"):
            for s in SIZES:
                render(f"{BRAND}/{name}.svg", s, s, f"{BRAND}/png/{name}-{s}.png", tmp)
        render(f"{BRAND}/koodam-social.svg", 1280, 640, f"{BRAND}/png/koodam-social.png", tmp)
    icons = [Image.open(f"{BRAND}/png/koodam-mark-{s}.png") for s in (16, 32, 64)]
    icons[-1].save(f"{BRAND}/favicon.ico", sizes=[(16, 16), (32, 32), (64, 64)], append_images=icons[:-1])
    return 0


if __name__ == "__main__":
    sys.exit(main())
