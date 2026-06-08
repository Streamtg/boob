#!/usr/bin/env python3
"""
TeleTorrent Bot v3.0 - Python
Descarga torrents desde magnet links y sube archivos a Telegram
Usa MTProto (Telethon) para archivos de hasta 2GB sin limites
"""

import asyncio
import hashlib
import json
import logging
import os
import re
import shutil
import signal
import sys
import tempfile
import time
import argparse
from datetime import datetime
from pathlib import Path
from typing import Optional

try:
    import libtorrent as lt
except ImportError:
    print("ERROR: pip install python-libtorrent")
    sys.exit(1)

try:
    from telethon import TelegramClient, events
except ImportError:
    print("ERROR: pip install telethon")
    sys.exit(1)

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
API_ID = 34280578
API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
CHANNEL_ID = -1003213143951
STORAGE_PATH = "./downloads"

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

torrent_temp_dir = tempfile.mkdtemp(prefix="teletorrent_")
def cleanup_temp():
    global torrent_temp_dir
    if os.path.exists(torrent_temp_dir):
        shutil.rmtree(torrent_temp_dir, ignore_errors=True)
        torrent_temp_dir = tempfile.mkdtemp(prefix="teletorrent_")

# ═══ CLIENTE TORRENT ═════════════════════════════════════════════════════════
class TorrentClient:
    def __init__(self, storage_path: str):
        self.storage_path = Path(storage_path)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        self.session = lt.session()
        self.session.listen_on(6881, 6891)
        self.session.apply_settings({
            "user_agent": "TeleTorrentBot/3.0",
            "download_rate_limit": 0,
            "upload_rate_limit": 0,
            "active_downloads": 3,
            "active_limit": 10,
            "max_connections": 200,
        })
        self.active_torrents: dict = {}
        log.info(f"Torrent client iniciado. Storage: {self.storage_path}")

    def add_magnet(self, magnet_uri: str) -> dict:
        params = {
            "save_path": str(self.storage_path),
            "storage_mode": lt.storage_mode_t.storage_mode_sparse,
        }
        handle = lt.add_magnet_uri(self.session, magnet_uri, params)
        timeout = 30
        while not handle.has_metadata():
            time.sleep(0.1)
            timeout -= 0.1
            if timeout <= 0:
                raise TimeoutError("Timeout esperando metadatos")
        info = handle.get_torrent_info()
        info_hash = str(info.info_hash())
        name = info.name()
        files = []
        total_size = 0
        for i in range(info.num_files()):
            fe = info.file_at(i)
            files.append({"path": fe.path, "size": fe.size, "index": i})
            total_size += fe.size
        handle.resume()
        self.active_torrents[info_hash] = {
            "handle": handle, "name": name, "files": files,
            "total_size": total_size, "started_at": time.time(), "cancelled": False,
        }
        log.info(f"Torrent: {name} ({self.format_size(total_size)})")
        return {"info_hash": info_hash, "name": name, "files": files, "total_size": total_size}

    def add_torrent_file(self, torrent_data: bytes) -> dict:
        torrent_path = f"{torrent_temp_dir}/temp.torrent"
        with open(torrent_path, "wb") as f:
            f.write(torrent_data)
        params = {
            "save_path": str(self.storage_path),
            "storage_mode": lt.storage_mode_t.storage_mode_sparse,
        }
        handle = lt.add_torrent(self.session, {"ti": lt.torrent_info(torrent_path), **params})
        timeout = 30
        while not handle.has_metadata():
            time.sleep(0.1)
            timeout -= 0.1
            if timeout <= 0:
                raise TimeoutError("Timeout esperando metadatos")
        info = handle.get_torrent_info()
        info_hash = str(info.info_hash())
        name = info.name()
        files = []
        total_size = 0
        for i in range(info.num_files()):
            fe = info.file_at(i)
            files.append({"path": fe.path, "size": fe.size, "index": i})
            total_size += fe.size
        handle.resume()
        self.active_torrents[info_hash] = {
            "handle": handle, "name": name, "files": files,
            "total_size": total_size, "started_at": time.time(), "cancelled": False,
        }
        return {"info_hash": info_hash, "name": name, "files": files, "total_size": total_size}

    def get_progress(self, info_hash: str) -> Optional[dict]:
        if info_hash not in self.active_torrents:
            return None
        t = self.active_torrents[info_hash]
        if t.get("cancelled"):
            return None
        handle = t["handle"]
        status = handle.status()
        return {
            "progress": status.progress * 100,
            "downloaded": status.total_download,
            "total": t["total_size"],
            "speed": status.download_rate,
            "state": self._state_str(status.state),
            "elapsed": time.time() - t["started_at"],
        }

    def cancel(self, info_hash: str):
        if info_hash in self.active_torrents:
            t = self.active_torrents[info_hash]
            t["cancelled"] = True
            self.session.remove_torrent(t["handle"])
            del self.active_torrents[info_hash]
            log.info(f"Cancelado: {t['name']}")

    def wait_complete(self, info_hash: str, callback=None):
        if info_hash not in self.active_torrents:
            return False
        t = self.active_torrents[info_hash]
        handle = t["handle"]
        last_p = -1
        while not t.get("cancelled", False):
            status = handle.status()
            p = int(status.progress * 100)
            if p != last_p:
                last_p = p
                if callback:
                    callback(self.get_progress(info_hash))
            if status.progress >= 1.0 and status.state != lt.torrent_status.checking_files:
                break
            time.sleep(1)
        return not t.get("cancelled", False)

    def get_completed_files(self, info_hash: str) -> list:
        if info_hash not in self.active_torrents:
            return []
        t = self.active_torrents[info_hash]
        completed = []
        for f in t["files"]:
            fp = self.storage_path / f["path"]
            if fp.exists() and fp.stat().st_size > 0:
                completed.append(str(fp))
        return completed

    @staticmethod
    def _state_str(state):
        return {0:"queued",1:"checking",2:"downloading",3:"downloading",
                4:"finished",5:"seeding",6:"allocating",7:"checking_fast"}.get(state,"unknown")

    @staticmethod
    def format_size(n: int) -> str:
        for u in ("B","KB","MB","GB","TB"):
            if n < 1024: return f"{n:.1f} {u}"
            n /= 1024
        return f"{n:.1f} PB"

    def close(self):
        for h in list(self.active_torrents.keys()):
            self.cancel(h)
        self.session.pause()

# ═══ BOT DE TELEGRAM ═════════════════════════════════════════════════════════
class TeleTorrentBot:
    def __init__(self):
        self.storage_path = Path(STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()
        self.torrent = TorrentClient(str(self.storage_path))
        self.active_tasks: dict = {}
        self.status_messages: dict = {}
        self.client = TelegramClient("teletorrent_session", API_ID, API_HASH,
                                     connection_retries=5, timeout=30)
        log.info("Bot inicializado")

    def _load_cache(self) -> dict:
        if self.cache_file.exists():
            try: return json.loads(self.cache_file.read_text())
            except: pass
        return {}

    def _save_cache(self):
        self.cache_file.write_text(json.dumps(self.file_cache, indent=2))

    def _get_file_id(self, file_path: str) -> Optional[str]:
        if not os.path.exists(file_path): return None
        with open(file_path, "rb") as f:
            md5 = hashlib.md5(f.read()).hexdigest()
        for e in self.file_cache.values():
            if e.get("md5") == md5: return e.get("file_id")
        return None

    def _cache_file_id(self, file_path: str, file_id: str):
        if not os.path.exists(file_path): return
        with open(file_path, "rb") as f:
            md5 = hashlib.md5(f.read()).hexdigest()
        self.file_cache[os.path.basename(file_path)] = {"md5": md5, "file_id": file_id}
        self._save_cache()

    async def start(self):
        log.info("Conectando a Telegram via MTProto...")
        await self.client.start(bot_token=BOT_TOKEN)
        me = await self.client.get_me()
        log.info(f"Bot: @{me.username} (ID: {me.id})")
        try:
            ch = await self.client.get_entity(CHANNEL_ID)
            log.info(f"Canal: {ch.title}")
        except Exception as e:
            log.warning(f"Canal no verificado: {e}")

        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_h(e):
            await e.reply(
                "*TeleTorrent Bot v3.0 (Python)*\n\n"
                "Envia un *magnet link* y descargo los archivos\n"
                "al canal via MTProto (sin limites de 50MB).\n\n"
                "*Comandos:*\n"
                "/help - ayuda\n"
                "/status - progreso\n"
                "/cancel - cancelar\n"
                "/cache - estado cache",
                parse_mode="md"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_h(e):
            cid = e.chat_id
            if cid not in self.active_tasks:
                await e.reply("*Sin descargas activas*", parse_mode="md")
                return
            t = self.active_tasks[cid]
            p = self.torrent.get_progress(t["info_hash"])
            if not p:
                await e.reply("*Sin descargas activas*", parse_mode="md")
                return
            bar = "=" * int(p["progress"]) + "-" * (20 - int(p["progress"]))
            await e.reply(
                f"*Descargando:* `{t['name']}`\n"
                f"{bar} `{p['progress']:.1f}%`\n"
                f"{TorrentClient.format_size(p['speed'])}/s | "
                f"{TorrentClient.format_size(p['downloaded'])} / "
                f"{TorrentClient.format_size(p['total'])}",
                parse_mode="md"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_h(e):
            cid = e.chat_id
            if cid not in self.active_tasks:
                await e.reply("*Nada que cancelar*", parse_mode="md")
                return
            t = self.active_tasks[cid]
            self.torrent.cancel(t["info_hash"])
            del self.active_tasks[cid]
            if cid in self.status_messages:
                try: await self.status_messages[cid].delete()
                except: pass
                del self.status_messages[cid]
            await e.reply("*Cancelado*", parse_mode="md")

        @self.client.on(events.NewMessage(pattern=r"^/cache$"))
        async def cache_h(e):
            await e.reply(f"*Cache:* {len(self.file_cache)} archivos", parse_mode="md")

        @self.client.on(events.NewMessage)
        async def msg_h(e):
            text = (e.message.text or "").strip()
            if text.startswith("/"): return
            if text.startswith("magnet:?xt=") or text.endswith(".torrent"):
                await self._start_download(e)

        log.info("Escuchando mensajes...")
        await self.client.run_until_disconnected()

    async def _start_download(self, event):
        cid = event.chat_id
        text = event.message.text.strip()
        if cid in self.active_tasks:
            await event.reply("*Ya hay una descarga activa.* Usa /cancel", parse_mode="md")
            return
        sm = await event.reply("*Iniciando...*", parse_mode="md")
        self.status_messages[cid] = sm
        try:
            if text.startswith("magnet:?xt="):
                if "&" in text: text = text[:text.index("&")]
                if not text.startswith("magnet:?xt="):
                    await sm.edit("*Magnet invalido*"); return
                await sm.edit("*Agregando magnet...*")
                info = self.torrent.add_magnet(text)
            else:
                await sm.edit("*Descargando .torrent...*")
                import requests
                r = requests.get(text, timeout=30)
                if r.status_code != 200:
                    await sm.edit(f"*Error HTTP {r.status_code}*"); return
                info = self.torrent.add_torrent_file(r.content)

            self.active_tasks[cid] = {
                "info_hash": info["info_hash"], "name": info["name"],
                "total_size": info["total_size"], "files": info["files"],
            }
            await sm.edit(f"*Agregado:* `{info['name']}`\n{self._pbar(0)} 0%")
            asyncio.create_task(self._monitor(cid, info["info_hash"]))
        except Exception as ex:
            log.error(f"Error: {ex}")
            await sm.edit(f"*Error:* `{ex}`")
            if cid in self.active_tasks: del self.active_tasks[cid]

    async def _monitor(self, cid, ih):
        try:
            def cb(p):
                asyncio.run_coroutine_threadsafe(self._upd_progress(cid, p), self.client.loop)
            ok = self.torrent.wait_complete(ih, cb)
            if not ok: return
            await self._upd_msg(cid, "*Descarga completa! Subiendo...*")
            files = self.torrent.get_completed_files(ih)
            await self._upload(cid, files)
        except Exception as ex:
            log.error(f"Monitor: {ex}")
            await self._upd_msg(cid, f"*Error:* `{ex}`")
        finally:
            if cid in self.active_tasks: del self.active_tasks[cid]
            if cid in self.status_messages:
                try: await self.status_messages[cid].delete()
                except: pass
                del self.status_messages[cid]

    async def _upd_progress(self, cid, p):
        if cid not in self.status_messages: return
        n = self.active_tasks.get(cid, {}).get("name", "")
        bar = self._pbar(int(p["progress"]))
        try:
            await self.status_messages[cid].edit(
                f"*Descargando:* `{n}`\n{bar} `{p['progress']:.1f}%`\n"
                f"{TorrentClient.format_size(p['speed'])}/s | "
                f"{TorrentClient.format_size(p['downloaded'])} / "
                f"{TorrentClient.format_size(p['total'])}"
            )
        except: pass

    async def _upd_msg(self, cid, text):
        if cid in self.status_messages:
            try: await self.status_messages[cid].edit(text)
            except: pass

    @staticmethod
    def _pbar(p):
        return "=" * min(p, 20) + "-" * (20 - min(p, 20))

    async def _upload(self, cid, files):
        up, fail = 0, 0
        for fp in files:
            if not os.path.exists(fp) or os.path.getsize(fp) == 0: continue
            fn = os.path.basename(fp); fs = os.path.getsize(fp)
            log.info(f"Subiendo: {fn} ({TorrentClient.format_size(fs)})")
            cached = self._get_file_id(fp)
            if cached:
                try:
                    await self.client.send_file(CHANNEL_ID, file=cached, caption=fn, force_document=True)
                    up += 1; continue
                except: pass
            try:
                await self._upd_msg(cid, f"*Subiendo:* `{fn}`\n{TorrentClient.format_size(fs)}")
                r = await self.client.send_file(CHANNEL_ID, file=fp, caption=fn, force_document=True)
                if hasattr(r, "id"): self._cache_file_id(fp, str(r.id))
                up += 1
                log.info(f"Enviado: {fn}")
            except Exception as e:
                log.error(f"Error subiendo {fn}: {e}")
                fail += 1
            await asyncio.sleep(0.5)
        s = f"*Completado!* {up} archivo(s) enviados."
        if fail: s = f"*Completado!* {up} enviados, {fail} fallaron."
        await self.client.send_message(cid, s)

    async def stop(self):
        log.info("Deteniendo...")
        self.torrent.close()
        await self.client.disconnect()

# ═══ MAIN ════════════════════════════════════════════════════════════════════
def main():
    p = argparse.ArgumentParser()
    p.add_argument("--token", default=BOT_TOKEN)
    p.add_argument("--channel", type=int, default=CHANNEL_ID)
    p.add_argument("--storage", default=STORAGE_PATH)
    a = p.parse_args()
    global BOT_TOKEN, CHANNEL_ID, STORAGE_PATH
    BOT_TOKEN, CHANNEL_ID, STORAGE_PATH = a.token, a.channel, a.storage
    log.info("=== TeleTorrent Bot v3.0 (Python) ===")
    bot = TeleTorrentBot()
    def sig(s, f):
        log.info(f"Senal {s}, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)
    signal.signal(signal.SIGINT, sig)
    signal.signal(signal.SIGTERM, sig)
    try: asyncio.run(bot.start())
    except KeyboardInterrupt: pass
    finally: cleanup_temp()

if __name__ == "__main__":
    main()
