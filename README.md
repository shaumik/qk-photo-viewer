# QK — burst photo culler

A fast, keyboard-first photo culler for macOS. Point it at your SD card,
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
explicit confirm moves them — recoverable — off the keeper list.

## Remote session (phone)

The app can serve the same UI over local Wi-Fi: scan a QR code on the
laptop, cull from your phone's browser — swipe to flip, swipe up to reject,
double-tap to zoom. No app install, no internet; state syncs live over a
WebSocket. Made for airplanes.

## Stack

- **[Wails v2](https://wails.io)** — Go backend, macOS WebKit webview UI
- **Go** does everything performance-critical: folder scan, ARW embedded-
  preview extraction, the prefetch ring, file moves
- **Vanilla HTML/CSS/JS** frontend, shared verbatim between the desktop
  window and phone remote sessions

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
- [ ] **2. Image pipeline** — ARW embedded-preview extraction, thumbnail
      service, prefetch ring in Go; wire the Wails backend into the UI.
- [ ] **3. File ops for real cards** — folder picker, macOS Trash
      integration, SD-card edge cases (slow readers, card removal mid-cull).
- [ ] **4. Package** — signed `.app` via GitHub Actions macOS runner.
- [ ] **5. Remote session** — LAN server, QR pairing, WebSocket sync.
