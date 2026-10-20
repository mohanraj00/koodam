# Brand

The Koodam mark is a K with a cluster of 3 hexagon pods in its open side. The K is the cluster, the factory (தொழிற்கூடம்). The pods are the agents that work in it. One pod is saffron: it is the agent at work now.

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
| `koodam-social.svg` | GitHub social preview, 1280 x 640. |
| `png/koodam-mark[-dark]-N.png` | PNG exports at 16, 32, 64, 128, 256 and 512 px. |
| `png/koodam-social.png` | The social preview as a PNG. |

To export the PNG files and `favicon.ico` again after an SVG change, run this command from the repo root. It needs Google Chrome and Pillow.

```bash
python3 hack/brand-export.py
```

## Colours

| Name | Hex | Use |
| --- | --- | --- |
| Ink | `#16213A` | The K and the pods on a light background. |
| Light | `#EEF1F6` | The K and the pods on a dark background. |
| Saffron | `#F5A524` | One pod only. |

## Clear space

Keep a clear space around the mark on all sides. The clear space is 1/8 of the mark width. On a 64 px mark, this is 8 px, the width of the K stem.

## Minimum size

The mark is 16 px minimum. At 16 px the pods show as one spot. Use the full mark at 32 px or more.

## Do not

- Do not use saffron on more than one pod, or on the K.
- Do not change the colours, the angle of the arms or the stroke width.
- Do not move, add or remove pods.
- Do not put the full colour mark on a background with low contrast. Use the dark or the one colour file.
- Do not add a shadow, an outline, a gradient or an effect.
- Do not put the mark in a heptagon or a wheel. That is the shape of a different project.
