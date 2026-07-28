# qk — quick kill for burst photos

A fast, keyboard-first photo culler. Point it at your SD card, rip through
hundreds of burst shots, reject the junk, keep the good ones. No import, no
catalog, no lag.

## Why

macOS gives you nothing decent for culling straight off an SD card, and the
Adobe options are slow because they decode full RAW files on demand. `qk`
cheats: it shows the **camera-embedded JPEG preview** inside each RAW
(10–50× faster to decode), prefetches the next several shots while you look
at the current one, and caches everything — so advancing to the next photo
is effectively instant.

## Install & run

Requires Node.js 18+ (`brew install node` if you don't have it).

```sh
git clone https://github.com/shaumik/qk-photo-viewer
cd qk-photo-viewer
npm install
npm start -- /Volumes/YOUR_SD_CARD/DCIM
```

Your browser opens automatically at `http://localhost:4242`.

## Keys

| Key | Action |
|---|---|
| `→` / `←` | next / previous |
| `X`, `Delete`, `D` | reject — instantly moves file(s) to `_rejected/` on the card |
| `Space` | keep & advance |
| `U` | undo last reject (or restore the photo you're on) |
| `Z` / click | zoom to 100%, move mouse to pan |
| `I` | EXIF overlay (camera, shutter, ISO, …) |
| `J` / `K` | jump to next / previous unreviewed |
| `?` | help |

## How deletion works

Nothing is ever deleted while you cull. Rejecting moves the file into a
`_rejected/` folder **on the card itself** — an instant same-volume rename,
fully undoable. When you're done, hit **Commit**: you see the summary
("keeping 214, rejecting 1,876 — free 38 GB?") and choose to delete the
rejects permanently, restore them all, or keep going.

## What it handles

- **JPEG** — resized previews via libvips (sharp)
- **RAW** — ARW, CR2/CR3, NEF, RAF, ORF, RW2, DNG, and more; previews come
  from the camera-embedded JPEG via exiftool
- **RAW+JPG pairs** — shown as one photo, rejected together
- **Video** — MP4/MOV play inline and cull with the same keys

## Notes

- Previews cache in `~/.cache/qk-photo-viewer` — first pass through a card
  warms the cache, everything after is instant.
- The server binds to localhost only; nothing leaves your machine.
