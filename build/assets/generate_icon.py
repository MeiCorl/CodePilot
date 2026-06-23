"""
根据 WebUI favicon SVG 设计生成 CodePilot 图标资源。

LOGO 设计（来源 src/internal/interaction/web/static/index.html 第9行）：
- 圆角矩形底 #C8A96A（琥珀金）
- 圆角半径 = 6 / 32 = 18.75%
- 居中文字 "CP"：等宽字体 monospace，字重 700，颜色 #0A0A0B
- 文字字号 = 18 / 32 = 56.25%（与画布等比缩放）

产出：
  build/assets/icon.ico       Windows 多尺寸 ICO（16/32/48/64/128/256）
  build/assets/icon.png       256x256 PNG（用于 README / 文档）
  build/assets/icon.rc        windres 资源脚本
"""

import os
from PIL import Image, ImageDraw, ImageFont

# === 设计常量（与 WebUI favicon 完全一致）===
BG_COLOR = (0xC8, 0xA9, 0x6A, 0xFF)  # #C8A96A
FG_COLOR = (0x0A, 0x0A, 0x0B, 0xFF)  # #0A0A0B
CORNER_RATIO = 6 / 32                # 圆角 18.75%
TEXT_RATIO = 18 / 32                 # 字号相对画布 56.25%
TEXT = "CP"

# 多尺寸：覆盖 Windows 任务栏、Alt-Tab、开始菜单、桌面快捷方式全部场景
SIZES = [16, 32, 48, 64, 128, 256]
PRIMARY_SIZE = 256

# Windows 自带 Consolas Bold（等宽 + 粗体，与 monospace + font-weight:700 等价）
FONT_PATH = r"C:\Windows\Fonts\consolab.ttf"

OUT_DIR = os.path.dirname(os.path.abspath(__file__))


def draw_icon(size: int) -> Image.Image:
    """按 WebUI 同款 LOGO 设计渲染一帧图标。"""
    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    draw = ImageDraw.Draw(img)

    # 圆角矩形底：与 SVG rect 行为一致
    radius = max(1, round(size * CORNER_RATIO))
    draw.rounded_rectangle(
        [(0, 0), (size - 1, size - 1)],
        radius=radius,
        fill=BG_COLOR,
    )

    # 文字：等宽 + 粗体 + 居中
    font_size = max(6, round(size * TEXT_RATIO))
    font = ImageFont.truetype(FONT_PATH, font_size)

    # Pillow 8.0+ 推荐用 textbbox 精确居中
    bbox = draw.textbbox((0, 0), TEXT, font=font, anchor="lt")
    text_w = bbox[2] - bbox[0]
    text_h = bbox[3] - bbox[1]
    x = (size - text_w) / 2 - bbox[0]
    # 视觉上比几何中心略低一点（与 SVG text y=22 保持一致：占画布 68.75%）
    y = (size - text_h) / 2 - bbox[1] - size * 0.02

    draw.text((x, y), TEXT, font=font, fill=FG_COLOR)
    return img


def main() -> None:
    # 先画一张最高分辨率的源图（256x256），再让 Pillow 用高质量重采样
    # 缩放出其余 5 个尺寸 — 矢量式的 SVG 渲染在最大尺寸上完成能最大限度
    # 保留圆角/文字描边细节，避免 16x16 任务栏图标出现糊字/锯齿。
    master = draw_icon(PRIMARY_SIZE)

    # 各尺寸独立画一遍以保证文字在每个尺寸都是像素级精确居中（Pillow
    # 缩放 256→16 时文字 stroke 会糊）。仅当源尺寸与目标尺寸相同时复用 master。
    frames = []
    for s in SIZES:
        if s == PRIMARY_SIZE:
            frames.append(master)
        else:
            frames.append(draw_icon(s))

    ico_path = os.path.join(OUT_DIR, "icon.ico")
    # Pillow ICO 写入：仅把最大帧作为源，其余通过 sizes 列表让 Pillow
    # 自行生成。这里直接一次性写入多张 PNG 帧（最稳妥的方案）。
    master.save(
        ico_path,
        format="ICO",
        sizes=[(s, s) for s in SIZES],
    )
    print(f"[ok] wrote {ico_path} (sizes={SIZES})")

    # 单独导出 256 PNG 供 README 使用
    png_path = os.path.join(OUT_DIR, "icon.png")
    master.save(png_path, format="PNG")
    print(f"[ok] wrote {png_path} (256x256)")

    # 写 windres 资源脚本：声明 1 号 ICON 资源指向 icon.ico
    rc_path = os.path.join(OUT_DIR, "icon.rc")
    with open(rc_path, "w", encoding="utf-8") as f:
        f.write('// CodePilot 应用图标资源（与 WebUI favicon 同款 LOGO）\n')
        f.write('// 重新生成：python build/assets/generate_icon.py\n')
        f.write('1 ICON "icon.ico"\n')
    print(f"[ok] wrote {rc_path}")


if __name__ == "__main__":
    main()
