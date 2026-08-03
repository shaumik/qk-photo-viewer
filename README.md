# QK

**Rip through burst photos straight off the SD card.** No import, no
catalog, no waiting for anything to load. Look at a shot, keep it or kill
it, next.

![The QK viewer](docs/screenshots/viewer.png)

You shot 800 frames of a bird and 30 of them matter. Photos.app wants to
import everything first. Bridge wants to think about each file. QK just
opens the folder and shows you the first photo before you've let go of the
mouse — because your camera already rendered a preview of every shot, and
QK uses it instead of chewing through the RAW.

## What it does

- **Opens your card directly** — pick the folder, start culling. Sony ARW
  and JPEG, with RAW+JPEG pairs treated as one photo.
- **Flip through a burst like a flipbook** — hold `→`. The next frames are
  already loaded before you get there.
- **Zoom that stays put** — `Z` for 1:1, and it stays locked while you
  arrow between near-identical frames to see which one nailed focus.
- **Nothing is deleted by accident** — `X` marks a reject (dim, red ✕,
  fully reversible). One explicit **Commit** moves the marked pairs to the
  Trash, where they're recoverable until you empty it.
- **Grid overview** — `G` to see the whole shoot at once.

![Grid overview](docs/screenshots/grid.png)

## Cull from your phone

<img src="docs/screenshots/phone.png" width="320" align="right" alt="Phone remote session">

Hit **📱 Phone**, scan the QR code, and the same session opens in your
phone's browser — swipe to flip, swipe up to reject, double-tap to zoom.
Marks sync live between phone and laptop, both can commit, and it all runs
over local Wi-Fi or a hotspot: no internet, no app to install, and nobody
without the QR code can connect.

Great for planes, couches, and anywhere you don't feel like hunching over
a trackpad.

<br clear="right">

## Keyboard

| Key | Action |
|---|---|
| `←` `→` | previous / next photo (hold to flip) |
| `X` | mark / unmark reject |
| `Z` or click | 1:1 zoom, move mouse to pan |
| `G` | grid overview |
| `⌘⏎` | commit rejects |
| `⌘O` | open a different folder |
| `?` | all shortcuts |

## Install

1. Download `QK-macos.zip` from the
   [latest release](../../releases/latest) and drag `QK.app` into
   Applications. Works on Intel and Apple Silicon.
2. First launch: macOS will balk because the app isn't code-signed.
   Click **Done**, then **System Settings → Privacy & Security → Open
   Anyway**. One time only.
3. Plug in your card, open the `DCIM` folder, cull.

---

<sub>Want to poke at the code? `make ui` runs the interface in a browser
with a fake shoot, `make test` runs the backend tests. Built with Go and
Wails.</sub>
