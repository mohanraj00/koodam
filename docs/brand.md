# Brand

The Koodam mark is a K made of 7 hexagon pods: a stem of 3 pods and 2 arms of 2 pods each. The cluster is its pods, and the workspace (தொழிற்கூடம்) is the people and the agents that work in it. One pod is saffron: it is the agent at work now. The same honeycomb fills the social preview and the blog images.

## Files

All files are in [`assets/brand/`](../assets/brand/). The SVG files are the source. Do not edit the PNG files or `favicon.ico` by hand.

| File | Use |
| --- | --- |
| `koodam-mark.svg` | Full colour, on a light background. |
| `koodam-mark-dark.svg` | Full colour, on a dark background. |
| `koodam-mark-mono.svg` | One colour (ink), on a light background. |
| `koodam-mark-mono-white.svg` | One colour (white), on a dark or a photo background. |
| `favicon.svg` | Browser tab icon. It changes with the light or dark colour scheme. |
| `favicon.ico` | Browser tab icon, 16, 32 and 64 px. |
| `koodam-social.svg` | GitHub social preview, 1280 x 640, with the name and the tagline. |
| `png/koodam-mark[-dark]-N.png` | PNG exports at 16, 32, 64, 128, 256 and 512 px. |
| `png/koodam-social.png` | The social preview as a PNG. |

To export the PNG files and `favicon.ico` again after an SVG change, run this command from the repo root. It needs Google Chrome and Pillow.

```bash
python3 hack/brand-export.py
```

## Colours

| Name | Hex | Use |
| --- | --- | --- |
| Ink | `#16213A` | The pods on a light background. The ground of the social preview. |
| Light | `#EEF1F6` | The pods on a dark background. |
| Saffron | `#F5A524` | One pod only: the end of the upper arm. |
| Raised ink | `#223052` | The pod field in the social preview. Not in the mark. |

## Clear space

Keep a clear space around the mark on all sides. The clear space is 1/8 of the mark width. The SVG files have this space in the 64 x 64 view box, so do not crop them.

## Minimum size

The mark is 16 px minimum. At 16 px the pods show as a texture with one saffron spot. Use the mark at 32 px or more if the K must be legible.

## Do not

- Do not use saffron on more than one pod.
- Do not change the colours, the pod size or the gap between the pods.
- Do not move, add or remove pods. Do not turn the pods: the flat side is at the top.
- Do not put the full colour mark on a background with low contrast. Use the dark or the one colour file.
- Do not add a shadow, an outline, a gradient or an effect.
- Do not put the mark in a heptagon or a wheel. That is the shape of a different project.
