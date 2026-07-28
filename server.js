#!/usr/bin/env node
// qk — fast burst photo culler.
// Usage: node server.js /path/to/sdcard/folder [--port 4242] [--no-open]

import http from 'node:http';
import fs from 'node:fs';
import fsp from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import crypto from 'node:crypto';
import { execFile } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import sharp from 'sharp';
import { exiftool } from 'exiftool-vendored';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PUBLIC_DIR = path.join(__dirname, 'public');
const REJECT_DIR_NAME = '_rejected';

const RAW_EXTS = new Set(['.arw', '.cr2', '.cr3', '.nef', '.nrw', '.raf', '.orf', '.rw2', '.dng', '.pef', '.srw', '.x3f']);
const JPEG_EXTS = new Set(['.jpg', '.jpeg']);
const IMG_EXTS = new Set([...JPEG_EXTS, '.png', '.webp', '.heic', '.heif', '.tif', '.tiff']);
const VIDEO_EXTS = new Set(['.mp4', '.mov', '.avi', '.m4v', '.mts', '.m2ts']);

// ---------- CLI ----------
const args = process.argv.slice(2);
let rootArg = null;
let port = 4242;
let autoOpen = true;
for (let i = 0; i < args.length; i++) {
  if (args[i] === '--port') port = parseInt(args[++i], 10);
  else if (args[i] === '--no-open') autoOpen = false;
  else if (!args[i].startsWith('-')) rootArg = args[i];
}
if (!rootArg) {
  console.error('Usage: qk <folder>  (e.g. qk /Volumes/SDCARD/DCIM)');
  process.exit(1);
}
const ROOT = path.resolve(rootArg);
if (!fs.existsSync(ROOT) || !fs.statSync(ROOT).isDirectory()) {
  console.error(`Not a directory: ${ROOT}`);
  process.exit(1);
}

const CACHE_DIR = path.join(os.homedir(), '.cache', 'qk-photo-viewer');
fs.mkdirSync(CACHE_DIR, { recursive: true });

// ---------- Library scan ----------
// An "item" is one logical photo: a RAW+JPG pair collapses into one entry.
let items = [];           // [{id, rel, name, kind, files:[rel...], size, mtime, rejected}]
let itemsById = new Map();
const undoStack = [];     // [{moves: [{from, to}]}]

function idFor(rel) {
  return crypto.createHash('sha1').update(rel).digest('hex').slice(0, 16);
}

async function walk(dir, out) {
  const entries = await fsp.readdir(dir, { withFileTypes: true });
  for (const e of entries) {
    if (e.name.startsWith('.')) continue;
    const full = path.join(dir, e.name);
    if (e.isDirectory()) {
      if (e.name === REJECT_DIR_NAME) continue;
      await walk(full, out);
    } else if (e.isFile()) {
      out.push(full);
    }
  }
}

async function scanLibrary() {
  const files = [];
  await walk(ROOT, files);

  // Group by (dir, basename) so IMG_001.ARW + IMG_001.JPG become one item.
  const groups = new Map();
  for (const full of files) {
    const ext = path.extname(full).toLowerCase();
    if (!RAW_EXTS.has(ext) && !IMG_EXTS.has(ext) && !VIDEO_EXTS.has(ext)) continue;
    const rel = path.relative(ROOT, full);
    const key = path.join(path.dirname(rel), path.basename(rel, path.extname(rel))).toLowerCase();
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(rel);
  }

  const list = [];
  for (const rels of groups.values()) {
    // Pick the file we display from: JPG beats RAW (faster), otherwise whatever is there.
    let display = rels.find(r => JPEG_EXTS.has(path.extname(r).toLowerCase()))
      || rels.find(r => IMG_EXTS.has(path.extname(r).toLowerCase()))
      || rels[0];
    const ext = path.extname(display).toLowerCase();
    let kind;
    if (VIDEO_EXTS.has(ext)) kind = 'video';
    else if (RAW_EXTS.has(ext)) kind = 'raw';
    else kind = 'image';
    const hasRaw = rels.some(r => RAW_EXTS.has(path.extname(r).toLowerCase()));

    let size = 0, mtime = 0;
    for (const r of rels) {
      const st = await fsp.stat(path.join(ROOT, r));
      size += st.size;
      mtime = Math.max(mtime, st.mtimeMs);
    }
    list.push({
      id: idFor(display),
      rel: display,
      name: path.basename(display),
      kind,
      hasRaw,
      files: rels,
      size,
      mtime,
      rejected: false,
    });
  }
  list.sort((a, b) => a.mtime - b.mtime || a.rel.localeCompare(b.rel));
  items = list;
  itemsById = new Map(items.map(it => [it.id, it]));
  return items;
}

// ---------- Preview pipeline ----------
// Cache key includes mtime+size so edits invalidate naturally.
const SIZES = { thumb: 320, fit: 2048 };
const inflight = new Map(); // cachePath -> promise
let extractQueue = Promise.resolve(); // serialize exiftool binary extraction

async function cachePathFor(item, variant) {
  const st = await fsp.stat(path.join(ROOT, item.rel));
  const key = crypto.createHash('sha1')
    .update(`${item.rel}|${st.size}|${Math.round(st.mtimeMs)}|${variant}`)
    .digest('hex');
  return path.join(CACHE_DIR, `${key}.jpg`);
}

// Pull the camera-embedded JPEG out of a RAW file. Largest available wins.
async function extractRawPreview(absPath, destPath) {
  const tmp = destPath + '.tmp-' + crypto.randomBytes(4).toString('hex') + '.jpg';
  const tags = ['JpgFromRaw', 'PreviewImage', 'OtherImage', 'ThumbnailImage'];
  for (const tag of tags) {
    try {
      await exiftool.extractBinaryTag(tag, absPath, tmp);
      await fsp.rename(tmp, destPath);
      return true;
    } catch {
      await fsp.rm(tmp, { force: true });
    }
  }
  return false;
}

async function getPreview(item, variant) {
  const cached = await cachePathFor(item, variant);
  if (fs.existsSync(cached)) return cached;
  if (inflight.has(cached)) return inflight.get(cached);

  const work = (async () => {
    const abs = path.join(ROOT, item.rel);
    let source = abs;
    let extractedPath = null;

    if (item.kind === 'raw') {
      extractedPath = await cachePathFor(item, 'extracted');
      if (!fs.existsSync(extractedPath)) {
        // exiftool-vendored is a single external process; serialize extractions
        // so a prefetch burst doesn't thrash it.
        const ok = await (extractQueue = extractQueue.then(
          () => extractRawPreview(abs, extractedPath),
          () => extractRawPreview(abs, extractedPath)
        ));
        if (!ok) throw new Error(`no embedded preview in ${item.rel}`);
      }
      source = extractedPath;
    }

    const tmp = cached + '.tmp-' + crypto.randomBytes(4).toString('hex') + '.jpg';
    const px = SIZES[variant] || SIZES.fit;
    await sharp(source, { failOn: 'none' })
      .rotate() // honor EXIF orientation
      .resize(px, px, { fit: 'inside', withoutEnlargement: true })
      .jpeg({ quality: variant === 'thumb' ? 70 : 82, mozjpeg: true })
      .toFile(tmp);
    await fsp.rename(tmp, cached);
    return cached;
  })().finally(() => inflight.delete(cached));

  inflight.set(cached, work);
  return work;
}

// ---------- Reject / undo ----------
async function moveItem(item, toRejected) {
  const moves = [];
  for (const rel of item.files) {
    const from = toRejected ? path.join(ROOT, rel) : path.join(ROOT, REJECT_DIR_NAME, rel);
    const to = toRejected ? path.join(ROOT, REJECT_DIR_NAME, rel) : path.join(ROOT, rel);
    if (!fs.existsSync(from)) continue;
    await fsp.mkdir(path.dirname(to), { recursive: true });
    await fsp.rename(from, to);
    moves.push({ from, to });
  }
  item.rejected = toRejected;
  return moves;
}

// ---------- EXIF info ----------
const exifCache = new Map();
async function getExif(item) {
  if (exifCache.has(item.id)) return exifCache.get(item.id);
  // Prefer the RAW for shooting data if there is one.
  const rel = item.files.find(r => RAW_EXTS.has(path.extname(r).toLowerCase())) || item.rel;
  const t = await exiftool.read(path.join(ROOT, rel));
  const info = {
    camera: [t.Make, t.Model].filter(Boolean).join(' '),
    lens: t.LensModel || t.LensID || '',
    shutter: t.ShutterSpeed || t.ExposureTime || '',
    aperture: t.Aperture ? `f/${t.Aperture}` : (t.FNumber ? `f/${t.FNumber}` : ''),
    iso: t.ISO ? `ISO ${t.ISO}` : '',
    focal: t.FocalLength || '',
    date: t.DateTimeOriginal ? String(t.DateTimeOriginal) : '',
    dims: t.ImageWidth && t.ImageHeight ? `${t.ImageWidth}×${t.ImageHeight}` : '',
  };
  exifCache.set(item.id, info);
  return info;
}

// ---------- HTTP ----------
const MIME = {
  '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css',
  '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg', '.png': 'image/png',
  '.svg': 'image/svg+xml', '.mp4': 'video/mp4', '.mov': 'video/quicktime',
  '.webp': 'image/webp', '.ico': 'image/x-icon',
};

function json(res, code, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(code, { 'Content-Type': 'application/json', 'Content-Length': Buffer.byteLength(body) });
  res.end(body);
}

function streamFile(req, res, absPath, contentType) {
  const st = fs.statSync(absPath);
  const range = req.headers.range;
  if (range) {
    const m = /bytes=(\d*)-(\d*)/.exec(range);
    let start = m[1] ? parseInt(m[1], 10) : 0;
    let end = m[2] ? parseInt(m[2], 10) : st.size - 1;
    end = Math.min(end, st.size - 1);
    res.writeHead(206, {
      'Content-Type': contentType,
      'Content-Range': `bytes ${start}-${end}/${st.size}`,
      'Accept-Ranges': 'bytes',
      'Content-Length': end - start + 1,
    });
    fs.createReadStream(absPath, { start, end }).pipe(res);
  } else {
    res.writeHead(200, {
      'Content-Type': contentType,
      'Content-Length': st.size,
      'Accept-Ranges': 'bytes',
      'Cache-Control': 'no-cache',
    });
    fs.createReadStream(absPath).pipe(res);
  }
}

async function readBody(req) {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const raw = Buffer.concat(chunks).toString('utf8');
  return raw ? JSON.parse(raw) : {};
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${req.headers.host}`);
  const p = url.pathname;
  try {
    // --- API ---
    if (p === '/api/list') {
      await scanLibrary();
      json(res, 200, {
        root: ROOT,
        items: items.map(({ id, rel, name, kind, hasRaw, files, size, mtime, rejected }) =>
          ({ id, rel, name, kind, hasRaw, nFiles: files.length, size, mtime, rejected })),
      });
      return;
    }

    if (p === '/api/reject' && req.method === 'POST') {
      const { id } = await readBody(req);
      const item = itemsById.get(id);
      if (!item) return json(res, 404, { error: 'unknown id' });
      if (!item.rejected) {
        const moves = await moveItem(item, true);
        undoStack.push({ id, moves });
      }
      json(res, 200, { ok: true, id, rejected: true });
      return;
    }

    if (p === '/api/restore' && req.method === 'POST') {
      const { id } = await readBody(req);
      const item = itemsById.get(id);
      if (!item) return json(res, 404, { error: 'unknown id' });
      if (item.rejected) await moveItem(item, false);
      json(res, 200, { ok: true, id, rejected: false });
      return;
    }

    if (p === '/api/undo' && req.method === 'POST') {
      const last = undoStack.pop();
      if (!last) return json(res, 200, { ok: false });
      const item = itemsById.get(last.id);
      if (item && item.rejected) await moveItem(item, false);
      json(res, 200, { ok: true, id: last.id, rejected: false });
      return;
    }

    if (p === '/api/empty-rejected' && req.method === 'POST') {
      // Permanently delete everything in _rejected. Only ever called from the
      // commit screen after an explicit confirmation.
      const dir = path.join(ROOT, REJECT_DIR_NAME);
      let freed = 0, count = 0;
      if (fs.existsSync(dir)) {
        const all = [];
        await walk(dir, all).catch(() => {});
        for (const f of all) { freed += (await fsp.stat(f)).size; count++; }
        await fsp.rm(dir, { recursive: true, force: true });
      }
      for (const it of items) if (it.rejected) it.rejected = false; // gone now
      undoStack.length = 0;
      await scanLibrary();
      json(res, 200, { ok: true, deleted: count, freed });
      return;
    }

    if (p.startsWith('/api/exif/')) {
      const item = itemsById.get(p.split('/').pop());
      if (!item) return json(res, 404, { error: 'unknown id' });
      json(res, 200, await getExif(item));
      return;
    }

    // --- Media ---
    if (p.startsWith('/preview/')) {
      const [, , id] = p.split('/');
      const item = itemsById.get(id);
      if (!item) { res.writeHead(404); res.end(); return; }
      const variant = url.searchParams.get('size') === 'thumb' ? 'thumb' : 'fit';
      try {
        const file = await getPreview(item, variant);
        res.writeHead(200, { 'Content-Type': 'image/jpeg', 'Cache-Control': 'max-age=3600' });
        fs.createReadStream(file).pipe(res);
      } catch (err) {
        // No decodable preview — send a placeholder so the UI keeps moving.
        res.writeHead(415, { 'Content-Type': 'text/plain' });
        res.end(String(err.message || err));
      }
      return;
    }

    if (p.startsWith('/file/')) {
      const item = itemsById.get(p.split('/').pop());
      if (!item) { res.writeHead(404); res.end(); return; }
      const base = item.rejected ? path.join(ROOT, REJECT_DIR_NAME) : ROOT;
      const abs = path.join(base, item.rel);
      streamFile(req, res, abs, MIME[path.extname(item.rel).toLowerCase()] || 'application/octet-stream');
      return;
    }

    // --- Static frontend ---
    let file = p === '/' ? '/index.html' : p;
    const abs = path.join(PUBLIC_DIR, path.normalize(file));
    if (abs.startsWith(PUBLIC_DIR) && fs.existsSync(abs) && fs.statSync(abs).isFile()) {
      streamFile(req, res, abs, MIME[path.extname(abs).toLowerCase()] || 'application/octet-stream');
      return;
    }

    res.writeHead(404); res.end('not found');
  } catch (err) {
    console.error(err);
    json(res, 500, { error: String(err.message || err) });
  }
});

server.listen(port, '127.0.0.1', async () => {
  const addr = `http://localhost:${port}`;
  await scanLibrary();
  console.log(`\n  qk — culling ${items.length} items in ${ROOT}`);
  console.log(`  ${addr}\n`);
  if (autoOpen && process.platform === 'darwin') {
    execFile('open', [addr], () => {});
  }
});

process.on('SIGINT', async () => {
  await exiftool.end();
  process.exit(0);
});
