# QK — burst photo culler

A fast, keyboard-first photo culler. Point it at your SD card,
rip through a burst, kill the soft frames, commit the rejects. No import
step, no catalog, no lag.

## Why

Burst shooting leaves 500–2,000 near-identical frames on a card, and macOS
gives you nothing good for triaging them in place: Photos.app forces a full
import first, Quick Look has no delete workflow, and Adobe Bridge fully
decodes every RAW while building a cache database — that's the lag.

QK does what the $139 pro tools do: **it never decodes the RAW.** Cameras
embed a full-size JPEG preview inside every ARW; QK extracts that preview
(milliseconds, not seconds) and prefetches the next several frames into a
ring buffer while you look at the current one. Culling feels instant
because, by the time you press →, the next frame is already in memory.

## Workflow

| Key | Action |
|---|---|
| `←` `→` | previous / next (hold to flip a burst like a flipbook) |
| `X` | mark / unmark reject — nothing is deleted yet |
| `Z` or click | 1:1 zoom; move the mouse to pan. Zoom persists across frames so you can compare focus within a burst |
| `G` | grid overview |
| `⌘⏎` | commit: review the rejects, then move them (whole RAW+JPEG pairs) into a rejects folder on the card |
| `?` | shortcut help |

Deletes are **mark-then-commit**: rejects dim in the filmstrip, and one
explicit confirm moves them — whole RAW+JPEG pairs — into the **macOS
Trash** (the card's own `.Trashes`, so it's an instant rename and shows up
in the Trash icon). On filesystems where that's not possible they go to a
`QK_REJECTS` folder on the card instead. Either way: recoverable until you
empty it, and a commit is never all-or-nothing — if the card dies mid-move,
whatever moved moved, the rest stays listed and marked.

## Remote session (phone)

The app can serve the same UI over local Wi-Fi: scan a QR code on the
laptop, cull from your phone's browser — swipe to flip, swipe up to reject,
double-tap to zoom. Works with **any phone** (Android or iOS — it's just a
web page, nothing to install), no internet needed; state syncs live over a
WebSocket. Made for airplanes.

## Stack

- **[Wails v2](https://wails.io)** — Go backend, native webview UI.
  Primary target is macOS; the same codebase also builds native Windows
  (WebView2) and Linux (WebKitGTK) apps
- **Go** does everything performance-critical: folder scan, ARW embedded-
  preview extraction, the prefetch ring, file moves
- **Vanilla HTML/CSS/JS** frontend, shared verbatim between the desktop
  window and phone remote sessions

## Install (macOS)

No toolchain needed:

1. Download `QK-macos.zip` from the latest
   [release](../../releases) — or, between releases, from the newest
   [Build workflow run](../../actions)'s artifacts.
2. Unzip and drag `QK.app` into Applications.
3. First launch only: **right-click the app → Open**. The app isn't
   code-signed yet, so macOS shows a warning once; opening it this way
   dismisses it permanently.

The build is a universal binary — Intel and Apple Silicon Macs both work.

## Development

```sh
make ui    # run the UI in a browser against a mock shoot (no Go needed)
make test  # Go tests for the library core (works on any OS)
make dev   # live-reload desktop app — needs macOS + wails CLI
make build # package the .app                — needs macOS + wails CLI
```

The frontend picks its backend at boot: the Wails bridge when running in
the app, otherwise a canvas-rendered mock shoot (five bursts of a bird at
dusk, soft frames included) so the whole workflow is exercisable in a bare
browser tab.

## Milestones

- [x] **1. UI + stack** — culling UI (desktop + touch), mock backend, Go
      library core: scan, RAW+JPEG pairing, commit-rejects. All tested.
- [x] **2. Image pipeline** — TIFF/IFD walker extracts embedded JPEG
      previews from ARW files (no RAW decode), EXIF thumbnails for the
      filmstrip, LRU caches with in-flight dedupe, background prefetch
      ring, `/api/thumb` + `/api/preview` served through the Wails asset
      server, folder picker, and the frontend bridge wired in.
- [x] **3. File ops for real cards** — real macOS Trash (volume
      `.Trashes` / `~/.Trash`, rejects-folder fallback), per-file commits
      that survive a dying card, card-removed detection with mark-keeping
      rescan, read-only card warning at open time, DCF folder rollover
      (`DCIM/100MSDCF` + `101MSDCF` culled together), slow-reader loading
      indicator.
- [x] **4. Package** — universal `.app` built by GitHub Actions on every
      push, attached to a GitHub Release on version tags. (Code signing /
      notarization still open — needs an Apple Developer account.)
- [ ] **5. Remote session** — LAN server, QR pairing, WebSocket sync.
