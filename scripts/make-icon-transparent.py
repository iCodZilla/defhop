"""Remove light border around media/icon.png; keep rounded navy body + teal/white art."""
from __future__ import annotations

from collections import deque
from pathlib import Path

from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parents[1]
ICON = ROOT / "media" / "icon.png"
BACKUP = ROOT / "media" / "icon-with-bg-backup.png"


def is_bg(r: int, g: int, b: int, a: int) -> bool:
    if a == 0:
        return True
    lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
    chroma = max(r, g, b) - min(r, g, b)
    if g > 140 and b > 120 and r < 120:
        return False
    if lum < 55:
        return False
    if chroma < 55 and lum > 70:
        return True
    if lum > 170 and chroma < 50:
        return True
    return False


def main() -> None:
    if not BACKUP.exists() and ICON.exists():
        Image.open(ICON).save(BACKUP)
    src_path = BACKUP if BACKUP.exists() else ICON

    img = Image.open(src_path).convert("RGBA")
    w, h = img.size
    px = img.load()
    visited = [[False] * h for _ in range(w)]
    q: deque[tuple[int, int]] = deque()
    dirs = ((1, 0), (-1, 0), (0, 1), (0, -1), (1, 1), (1, -1), (-1, 1), (-1, -1))

    def push(x: int, y: int) -> None:
        if 0 <= x < w and 0 <= y < h and not visited[x][y]:
            r, g, b, a = px[x, y]
            if is_bg(r, g, b, a):
                visited[x][y] = True
                q.append((x, y))

    for x in range(w):
        push(x, 0)
        push(x, h - 1)
    for y in range(h):
        push(0, y)
        push(w - 1, y)
    while q:
        x, y = q.popleft()
        px[x, y] = (0, 0, 0, 0)
        for dx, dy in dirs:
            push(x + dx, y + dy)

    xs, ys = [], []
    for y in range(h):
        for x in range(w):
            if px[x, y][3] > 0:
                xs.append(x)
                ys.append(y)
    minx, maxx = min(xs), max(xs)
    miny, maxy = min(ys), max(ys)

    pad = 1
    radius = int(min(w, h) * 0.22)
    mask = Image.new("L", (w, h), 0)
    ImageDraw.Draw(mask).rounded_rectangle(
        [minx + pad, miny + pad, maxx - pad, maxy - pad], radius=radius, fill=255
    )

    out = Image.new("RGBA", (w, h), (0, 0, 0, 0))
    mp, bp, op = mask.load(), img.load(), out.load()
    for y in range(h):
        for x in range(w):
            r, g, b, a = bp[x, y]
            m = mp[x, y]
            if m == 0 or a == 0:
                op[x, y] = (0, 0, 0, 0)
                continue
            lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
            chroma = max(r, g, b) - min(r, g, b)
            if m < 255 and chroma < 45 and lum > 90:
                op[x, y] = (0, 0, 0, 0)
            else:
                op[x, y] = (r, g, b, int(a * m / 255))

    out.save(ICON, "PNG")
    p = Image.open(ICON).convert("RGBA").load()
    for pt in ((0, 0), (w - 1, 0), (0, h - 1), (w - 1, h - 1), (w // 2, h // 2)):
        print(pt, p[pt])
    print("saved", ICON, ICON.stat().st_size)


if __name__ == "__main__":
    main()
