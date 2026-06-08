#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v14.0
- Progreso real de descarga Y subida
- Conversión automática de video para Telegram (reproducible)
- Soporte magnet, torrents, URLs HTTP
"""

import asyncio
import base64
import hashlib
import json
import logging
import math
import os
import re
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path
from typing import Optional, Dict, List
from urllib.parse import unquote, urlparse

import requests
from telethon import TelegramClient, events
from telethon.errors import (
    FloodWaitError,
    MessageNotModifiedError,
    MessageIdInvalidError,
    ChatWriteForbiddenError,
)
from telethon.tl.types import DocumentAttributeFilename, DocumentAttributeVideo

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURACIÓN
# ═══════════════════════════════════════════════════════════════════════════════
class Config:
    API_ID          = 34280578
    API_HASH        = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN       = "8835976877:AAESuq6cKUvWOnwOCfn-I0Xb3zx_raJgYMQ"  # ← NUEVO TOKEN
    CHANNEL_ID      = -1003213143951
    STORAGE_PATH    = "./downloads"
    ARIA2_PORT      = 6800
    ARIA2_SECRET    = ""
    UPDATE_INTERVAL = 4           # segundos entre updates de progreso
    MAX_FILE_SIZE   = 2 * 1024**3 # 2 GB
    MONITOR_TIMEOUT = 7200        # 2 horas máx descarga
    MAX_STALL_TIME  = 600         # 10 min sin progreso

    # Video
    VIDEO_EXTENSIONS = {
        ".mp4", ".mkv", ".avi", ".mov", ".wmv", ".flv",
        ".webm", ".m4v", ".ts", ".m2ts", ".mpeg", ".mpg",
        ".3gp", ".ogv", ".divx", ".xvid", ".rmvb", ".rm"
    }
    # Formatos ya compatibles con Telegram (no necesitan conversión)
    TELEGRAM_NATIVE = {".mp4"}
    # CRF para conversión (18=alta calidad, 28=menor tamaño)
    VIDEO_CRF       = 23
    # Preset ffmpeg (ultrafast/fast/medium/slow)
    VIDEO_PRESET    = "fast"

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING
# ═══════════════════════════════════════════════════════════════════════════════
def setup_logging() -> logging.Logger:
    fmt = logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S"
    )
    logger = logging.getLogger("TeleTorrent")
    logger.setLevel(logging.INFO)

    sh = logging.StreamHandler(sys.stdout)
    sh.setFormatter(fmt)
    logger.addHandler(sh)

    fh = logging.FileHandler("teletorrent.log", encoding="utf-8")
    fh.setFormatter(fmt)
    logger.addHandler(fh)

    return logger

log = setup_logging()

# ═══════════════════════════════════════════════════════════════════════════════
# UTILIDADES
# ═══════════════════════════════════════════════════════════════════════════════
class Utils:

    @staticmethod
    def format_size(n) -> str:
        try:
            n = max(0.0, float(n or 0))
        except:
            n = 0.0
        for unit in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024.0:
                return f"{n:.2f} {unit}"
            n /= 1024.0
        return f"{n:.2f} PB"

    @staticmethod
    def format_speed(bps) -> str:
        return f"{Utils.format_size(bps)}/s"

    @staticmethod
    def format_eta(seconds) -> str:
        try:
            s = int(seconds)
            if s <= 0 or s > 86400 * 7:
                return "∞"
            h, r = divmod(s, 3600)
            m, s = divmod(r, 60)
            if h:   return f"{h}h {m}m"
            if m:   return f"{m}m {s}s"
            return  f"{s}s"
        except:
            return "∞"

    @staticmethod
    def progress_bar(p: float, width: int = 20) -> str:
        try:
            p = min(100.0, max(0.0, float(p)))
        except:
            p = 0.0
        filled = int(p / (100 / width))
        return "█" * filled + "░" * (width - filled)

    @staticmethod
    def sanitize_filename(name: str) -> str:
        name = re.sub(r'[<>:"/\\|?*\x00-\x1f]', "_", name)
        return name.strip(". ")[:200] or "descarga"

    @staticmethod
    def get_md5(fp: str) -> Optional[str]:
        try:
            h = hashlib.md5()
            with open(fp, "rb") as f:
                for chunk in iter(lambda: f.read(65536), b""):
                    h.update(chunk)
            return h.hexdigest()
        except:
            return None

    @staticmethod
    def extract_name_from_magnet(magnet: str) -> str:
        dn = re.search(r"[?&]dn=([^&]+)", magnet)
        if dn:
            try:
                return unquote(dn.group(1)).replace("+", " ")
            except:
                pass
        ih = re.search(r"xt=urn:btih:([a-fA-F0-9]{40}|[A-Z2-7]{32})", magnet)
        return f"Torrent_{ih.group(1)[:8]}" if ih else "Descarga_Magnet"

    @staticmethod
    def is_video(fp: str) -> bool:
        return Path(fp).suffix.lower() in Config.VIDEO_EXTENSIONS

    @staticmethod
    def needs_conversion(fp: str) -> bool:
        """True si el video necesita conversión para Telegram"""
        suffix = Path(fp).suffix.lower()
        if suffix not in Config.VIDEO_EXTENSIONS:
            return False
        if suffix not in Config.TELEGRAM_NATIVE:
            return True
        # MP4 puede necesitar conversión si el codec no es H264/AAC
        return False  # los mp4 los intentamos directo primero

# ═══════════════════════════════════════════════════════════════════════════════
# CONVERSOR DE VIDEO
# ═══════════════════════════════════════════════════════════════════════════════
class VideoConverter:
    """Convierte videos a MP4 H264/AAC compatible con Telegram"""

    @staticmethod
    def ffmpeg_available() -> bool:
        return shutil.which("ffmpeg") is not None

    @staticmethod
    def get_video_info(fp: str) -> dict:
        """Obtiene info del video con ffprobe"""
        try:
            cmd = [
                "ffprobe", "-v", "quiet",
                "-print_format", "json",
                "-show_streams", "-show_format",
                fp
            ]
            result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
            if result.returncode == 0:
                data = json.loads(result.stdout)
                info = {"duration": 0, "width": 0, "height": 0,
                        "vcodec": "", "acodec": ""}

                fmt = data.get("format", {})
                info["duration"] = int(float(fmt.get("duration", 0)))

                for stream in data.get("streams", []):
                    if stream.get("codec_type") == "video":
                        info["width"]  = stream.get("width", 0)
                        info["height"] = stream.get("height", 0)
                        info["vcodec"] = stream.get("codec_name", "")
                    elif stream.get("codec_type") == "audio":
                        info["acodec"] = stream.get("codec_name", "")

                return info
        except Exception as e:
            log.warning(f"ffprobe error: {e}")
        return {"duration": 0, "width": 0, "height": 0, "vcodec": "", "acodec": ""}

    @staticmethod
    async def convert(
        fp: str,
        progress_callback=None
    ) -> Optional[str]:
        """
        Convierte video a MP4 H264/AAC.
        progress_callback(percent: float, speed: str, eta: str)
        Retorna ruta del archivo convertido o None si falla.
        """
        if not VideoConverter.ffmpeg_available():
            log.error("❌ ffmpeg no instalado")
            return None

        src  = Path(fp)
        dst  = src.with_suffix(".converted.mp4")

        # Obtener info para thumbnail y duración
        info = VideoConverter.get_video_info(fp)
        log.info(
            f"🎬 Convirtiendo: {src.name} "
            f"({info['width']}x{info['height']}, "
            f"{info['vcodec']}/{info['acodec']}, "
            f"{Utils.format_eta(info['duration'])})"
        )

        # Comando ffmpeg con progreso
        cmd = [
            "ffmpeg", "-y",
            "-i", str(src),
            # Video: H264 con CRF
            "-c:v", "libx264",
            "-crf", str(Config.VIDEO_CRF),
            "-preset", Config.VIDEO_PRESET,
            "-profile:v", "high",
            "-level", "4.1",
            "-pix_fmt", "yuv420p",   # Compatibilidad máxima
            # Audio: AAC
            "-c:a", "aac",
            "-b:a", "192k",
            "-ac", "2",
            # Metadatos para Telegram
            "-movflags", "+faststart",  # Streaming inmediato
            # Progreso
            "-progress", "pipe:1",
            "-nostats",
            str(dst)
        ]

        try:
            proc = await asyncio.create_subprocess_exec(
                *cmd,
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE
            )

            duration_secs = info["duration"] or 1
            last_time     = 0.0

            # Leer progreso de stdout (ffmpeg -progress pipe:1)
            while True:
                try:
                    line = await asyncio.wait_for(
                        proc.stdout.readline(), timeout=60
                    )
                except asyncio.TimeoutError:
                    log.warning("ffmpeg sin salida por 60s")
                    break

                if not line:
                    break

                line = line.decode("utf-8", errors="ignore").strip()

                if line.startswith("out_time_ms="):
                    try:
                        ms       = int(line.split("=")[1])
                        cur_secs = ms / 1_000_000
                        percent  = min(99.0, (cur_secs / duration_secs) * 100)

                        if progress_callback and abs(cur_secs - last_time) >= 2:
                            last_time = cur_secs
                            remaining = duration_secs - cur_secs
                            await progress_callback(percent, remaining)

                    except:
                        pass

                if line == "progress=end":
                    break

            await proc.wait()

            if proc.returncode != 0:
                stderr = await proc.stderr.read()
                log.error(f"ffmpeg error:\n{stderr.decode(errors='ignore')[-500:]}")
                if dst.exists():
                    dst.unlink()
                return None

            if not dst.exists() or dst.stat().st_size == 0:
                log.error("ffmpeg: archivo de salida vacío")
                return None

            log.info(f"✅ Conversión completada: {dst.name} ({Utils.format_size(dst.stat().st_size)})")

            # Eliminar original
            try:
                src.unlink()
            except:
                pass

            return str(dst)

        except Exception as e:
            log.error(f"Error en conversión: {e}", exc_info=True)
            if dst.exists():
                try: dst.unlink()
                except: pass
            return None

    @staticmethod
    def generate_thumbnail(fp: str) -> Optional[str]:
        """Genera miniatura del video"""
        try:
            info   = VideoConverter.get_video_info(fp)
            dur    = info["duration"]
            ts     = min(dur * 0.1, 10) if dur > 0 else 1  # 10% o 10s

            thumb  = str(Path(fp).with_suffix(".thumb.jpg"))
            cmd    = [
                "ffmpeg", "-y",
                "-ss", str(ts),
                "-i", fp,
                "-vframes", "1",
                "-vf", "scale=320:-1",
                "-q:v", "5",
                thumb
            ]
            result = subprocess.run(cmd, capture_output=True, timeout=30)
            if result.returncode == 0 and os.path.exists(thumb):
                return thumb
        except Exception as e:
            log.warning(f"Thumbnail error: {e}")
        return None

# ═══════════════════════════════════════════════════════════════════════════════
# ARIA2 RPC
# ═══════════════════════════════════════════════════════════════════════════════
class Aria2RPC:

    def __init__(self):
        self.url     = f"http://127.0.0.1:{Config.ARIA2_PORT}/jsonrpc"
        self._id     = 0
        self.ready   = False
        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})

    def wait_ready(self, max_attempts: int = 40) -> bool:
        log.info("⏳ Esperando aria2 RPC...")
        for i in range(max_attempts):
            try:
                r = self.session.post(
                    self.url,
                    json={"jsonrpc":"2.0","id":0,"method":"aria2.getVersion","params":[]},
                    timeout=3
                )
                if r.status_code == 200 and "result" in r.json():
                    ver = r.json()["result"].get("version","?")
                    log.info(f"✓ aria2 RPC listo (v{ver})")
                    self.ready = True
                    return True
            except:
                pass
            time.sleep(1)
            if i % 10 == 9:
                log.info(f"  aria2 RPC... ({i+1}/{max_attempts})")
        log.error("❌ aria2 RPC no responde")
        return False

    def _call(self, method: str, params: list = None, retries: int = 3):
        params = params or []
        if Config.ARIA2_SECRET:
            params = [f"token:{Config.ARIA2_SECRET}"] + params

        for attempt in range(retries):
            try:
                self._id += 1
                r = self.session.post(
                    self.url,
                    json={"jsonrpc":"2.0","id":self._id,"method":method,"params":params},
                    timeout=30
                )
                if r.status_code == 200:
                    data = r.json()
                    if "error" in data:
                        log.warning(f"aria2 [{method}]: {data['error'].get('message','?')}")
                        return None
                    return data.get("result")
            except requests.exceptions.ConnectionError:
                if attempt < retries - 1:
                    time.sleep(2)
                    self.wait_ready(10)
            except Exception as e:
                log.warning(f"aria2 excepción [{method}]: {e}")
        return None

    def add_uri(self, uris: list, opts: dict = None) -> Optional[str]:
        params = [uris]
        if opts:
            params.append({k: str(v) for k, v in opts.items()})
        r = self._call("aria2.addUri", params)
        return r if isinstance(r, str) else None

    def add_torrent(self, b64: str, opts: dict = None) -> Optional[str]:
        r = self._call("aria2.addTorrent", [b64, [], opts or {}])
        return r if isinstance(r, str) else None

    def get_status(self, gid: str) -> Optional[dict]:
        keys = ["gid","status","totalLength","completedLength",
                "downloadSpeed","uploadSpeed","files","bittorrent",
                "errorCode","errorMessage"]
        r = self._call("aria2.tellStatus", [gid, keys])
        return r if isinstance(r, dict) else None

    def remove(self, gid: str):
        self._call("aria2.pause",       [gid])
        time.sleep(0.3)
        self._call("aria2.remove",      [gid])
        self._call("aria2.forceRemove", [gid])
        self._call("aria2.removeDownloadResult", [gid])

# ═══════════════════════════════════════════════════════════════════════════════
# ARIA2 MANAGER
# ═══════════════════════════════════════════════════════════════════════════════
class Aria2Manager:

    @staticmethod
    def kill_existing():
        try:
            subprocess.run(["pkill","-9","-f","aria2c"], timeout=5)
            time.sleep(1.5)
        except:
            pass

    @staticmethod
    def is_running() -> bool:
        try:
            return subprocess.run(
                ["pgrep","-f","aria2c"], capture_output=True, timeout=5
            ).returncode == 0
        except:
            return False

    @staticmethod
    def start(storage: str) -> bool:
        if not shutil.which("aria2c"):
            log.error("❌ aria2c no instalado: sudo apt-get install -y aria2")
            return False

        Aria2Manager.kill_existing()
        sp = Path(storage).absolute()
        sp.mkdir(parents=True, exist_ok=True)

        session = sp / "aria2.session"
        session.touch()

        cmd = [
            "aria2c",
            "--enable-rpc=true",
            "--rpc-listen-all=true",
            f"--rpc-listen-port={Config.ARIA2_PORT}",
            "--rpc-allow-origin-all=true",
            f"--dir={sp}",
            "--max-concurrent-downloads=5",
            "--max-connection-per-server=8",
            "--split=8",
            "--min-split-size=5M",
            "--max-tries=10",
            "--retry-wait=5",
            "--connect-timeout=30",
            "--timeout=60",
            "--continue=true",
            "--allow-overwrite=true",
            "--auto-file-renaming=false",
            "--disk-cache=64M",
            "--file-allocation=none",
            "--enable-dht=true",
            "--enable-peer-exchange=true",
            "--bt-enable-lpd=true",
            "--bt-max-peers=100",
            "--seed-time=0",
            "--bt-stop-timeout=300",
            "--follow-torrent=mem",
            f"--save-session={session}",
            "--save-session-interval=30",
            f"--input-file={session}",
            "--daemon=true",
            f"--log={sp / 'aria2.log'}",
            "--log-level=notice",
        ]

        log.info("🚀 Iniciando aria2c...")
        try:
            subprocess.Popen(cmd, stdout=subprocess.DEVNULL,
                             stderr=subprocess.DEVNULL, start_new_session=True)
            time.sleep(2)
            if Aria2Manager.is_running():
                log.info("✓ aria2c corriendo")
                return True
            log.error("❌ aria2c no inició")
            return False
        except FileNotFoundError:
            log.error("❌ aria2c no encontrado")
            return False
        except Exception as e:
            log.error(f"❌ aria2c: {e}")
            return False

    @staticmethod
    def stop():
        try:
            subprocess.run(["pkill","-SIGTERM","-f","aria2c"], timeout=5)
            time.sleep(2)
            if Aria2Manager.is_running():
                subprocess.run(["pkill","-9","-f","aria2c"], timeout=5)
        except:
            pass

# ═══════════════════════════════════════════════════════════════════════════════
# TAREA DE DESCARGA
# ═══════════════════════════════════════════════════════════════════════════════
class DownloadTask:
    def __init__(self, gid: str, name: str, chat_id: int, source: str = ""):
        self.gid          = gid
        self.name         = name
        self.chat_id      = chat_id
        self.source       = source
        self.started_at   = time.time()
        self.status_msg   = None
        self.monitor_task = None

    @property
    def elapsed(self) -> float:
        return time.time() - self.started_at

    def is_timed_out(self) -> bool:
        return self.elapsed > Config.MONITOR_TIMEOUT

# ═══════════════════════════════════════════════════════════════════════════════
# BOT PRINCIPAL
# ═══════════════════════════════════════════════════════════════════════════════
class TeleTorrentBot:

    def __init__(self):
        self.storage = Path(Config.STORAGE_PATH).absolute()
        self.storage.mkdir(parents=True, exist_ok=True)

        self.aria2: Optional[Aria2RPC] = None
        self.tasks: Dict[int, DownloadTask] = {}

        self.client = TelegramClient(
            str(self.storage / "session"),
            Config.API_ID,
            Config.API_HASH,
            connection_retries=10,
            retry_delay=5,
            timeout=30,
        )

        # Verificar ffmpeg
        if VideoConverter.ffmpeg_available():
            log.info("✓ ffmpeg disponible — conversión de video activa")
        else:
            log.warning("⚠️ ffmpeg NO disponible — videos sin conversión")
            log.warning("   Instala: sudo apt-get install -y ffmpeg")

    # ─── INICIO ──────────────────────────────────────────────────────────────

    async def start(self):
        if not Aria2Manager.start(str(self.storage)):
            sys.exit(1)

        self.aria2 = Aria2RPC()
        if not self.aria2.wait_ready():
            Aria2Manager.stop()
            sys.exit(1)

        log.info("🔌 Conectando Telegram...")
        await self.client.start(bot_token=Config.BOT_TOKEN)

        me = await self.client.get_me()
        log.info(f"✓ Bot: @{me.username} (ID: {me.id})")

        try:
            ch = await self.client.get_entity(Config.CHANNEL_ID)
            log.info(f"✓ Canal: {ch.title}")
        except Exception as e:
            log.warning(f"⚠️ Canal: {e}")

        self._register_handlers()
        log.info("✅ Bot listo — esperando mensajes...")
        await self.client.run_until_disconnected()

    # ─── HANDLERS ────────────────────────────────────────────────────────────

    def _register_handlers(self):

        @self.client.on(events.NewMessage(pattern=r"^/(?:start|help)$"))
        async def cmd_help(event):
            ffmpeg_status = "✅ Activo" if VideoConverter.ffmpeg_available() else "❌ No instalado"
            await event.reply(
                "🚀 **TeleTorrent Bot v14.0**\n\n"
                "Descarga → Convierte → Sube a Telegram\n\n"
                "**📥 Envíame:**\n"
                "• Un **magnet link**\n"
                "• Una **URL HTTP/HTTPS**\n"
                "• Un archivo **.torrent**\n\n"
                "**⚙️ Comandos:**\n"
                "`/status` — progreso actual\n"
                "`/cancel` — cancelar descarga\n"
                "`/storage` — espacio en disco\n\n"
                f"**🎬 Conversión de video:** {ffmpeg_status}\n"
                "Videos se convierten a MP4 H264 para\n"
                "reproducirse en Telegram.",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def cmd_status(event):
            cid = event.chat_id
            if cid not in self.tasks:
                await event.reply("⏸ **Sin descargas activas**", parse_mode="markdown")
                return
            task = self.tasks[cid]
            info = self.aria2.get_status(task.gid)
            if not info:
                await event.reply("⚠️ No se pudo obtener estado", parse_mode="markdown")
                return
            await event.reply(self._build_progress_text(task, info), parse_mode="markdown")

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cmd_cancel(event):
            cid = event.chat_id
            if cid not in self.tasks:
                await event.reply("ℹ️ Nada que cancelar", parse_mode="markdown")
                return
            await self._cancel_task(cid)
            await event.reply("✅ **Cancelado**", parse_mode="markdown")

        @self.client.on(events.NewMessage(pattern=r"^/storage$"))
        async def cmd_storage(event):
            try:
                st    = shutil.disk_usage(self.storage)
                files = [f for f in self.storage.rglob("*") if f.is_file()]
                fsize = sum(f.stat().st_size for f in files)
                await event.reply(
                    f"💾 **Almacenamiento**\n\n"
                    f"Disco total: `{Utils.format_size(st.total)}`\n"
                    f"Usado:       `{Utils.format_size(st.used)}`\n"
                    f"**Libre:     `{Utils.format_size(st.free)}`**\n\n"
                    f"Archivos:    `{len(files)}`\n"
                    f"Tamaño:      `{Utils.format_size(fsize)}`",
                    parse_mode="markdown"
                )
            except Exception as e:
                await event.reply(f"❌ {e}")

        @self.client.on(events.NewMessage)
        async def cmd_msg(event):
            text = (event.message.text or "").strip()
            if text.startswith("/"):
                return
            if text.lower().startswith("magnet:?xt="):
                await self._handle_magnet(event, text)
            elif re.match(r"https?://", text, re.IGNORECASE):
                await self._handle_url(event, text)
            elif event.message.document:
                await self._handle_document(event)

    # ─── MANEJADORES ─────────────────────────────────────────────────────────

    async def _handle_magnet(self, event, magnet: str):
        cid = event.chat_id
        if cid in self.tasks:
            await event.reply("⚠️ Ya hay descarga activa. Usa `/cancel`.", parse_mode="markdown")
            return

        if not re.search(r"xt=urn:btih:", magnet, re.IGNORECASE):
            await event.reply("❌ Magnet inválido", parse_mode="markdown")
            return

        name = Utils.extract_name_from_magnet(magnet)
        sm   = await event.reply(
            f"⏳ **Procesando magnet...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        gid = self.aria2.add_uri([magnet], {
            "dir": str(self.storage), "seed-time": "0", "bt-stop-timeout": "300"
        })

        if not gid:
            await self._safe_edit(sm, "❌ **No se pudo agregar el magnet**")
            return

        await self._start_task(cid, gid, name, "magnet", sm)

    async def _handle_url(self, event, url: str):
        cid = event.chat_id
        if cid in self.tasks:
            await event.reply("⚠️ Ya hay descarga activa. Usa `/cancel`.", parse_mode="markdown")
            return

        try:
            p    = urlparse(url)
            name = Utils.sanitize_filename(unquote(p.path.split("/")[-1])) or "descarga"
        except:
            name = "descarga"

        sm = await event.reply(
            f"⏳ **Iniciando descarga...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        gid = self.aria2.add_uri([url], {
            "dir": str(self.storage), "out": name,
            "max-connection-per-server": "8", "split": "8",
            "min-split-size": "5M", "max-tries": "10",
        })

        if not gid:
            await self._safe_edit(sm, "❌ **No se pudo agregar la URL**")
            return

        await self._start_task(cid, gid, name, "url", sm)

    async def _handle_document(self, event):
        cid = event.chat_id
        if cid in self.tasks:
            await event.reply("⚠️ Ya hay descarga activa. Usa `/cancel`.", parse_mode="markdown")
            return

        doc  = event.message.document
        name = "archivo"
        if doc.attributes:
            for a in doc.attributes:
                if isinstance(a, DocumentAttributeFilename):
                    name = a.file_name
                    break

        is_torrent = name.lower().endswith(".torrent")
        sm = await event.reply(
            f"📥 **Descargando de Telegram...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        tmp = self.storage / f"tmp_{int(time.time())}_{name}"
        try:
            await event.message.download_media(str(tmp))

            if not tmp.exists() or tmp.stat().st_size == 0:
                await self._safe_edit(sm, "❌ Archivo vacío")
                return

            if is_torrent:
                b64 = base64.b64encode(tmp.read_bytes()).decode()
                try: tmp.unlink()
                except: pass

                gid = self.aria2.add_torrent(b64, {
                    "dir": str(self.storage), "seed-time": "0"
                })
                if not gid:
                    await self._safe_edit(sm, "❌ No se pudo procesar el torrent")
                    return
                await self._start_task(cid, gid, name.replace(".torrent",""), "torrent", sm)
            else:
                # Subir directamente
                await self._safe_edit(sm, f"✅ Descargado. Procesando...\n`{name[:50]}`")
                await self._process_and_upload(cid, [str(tmp)], sm)
        except Exception as e:
            log.error(f"Error documento: {e}", exc_info=True)
            await self._safe_edit(sm, f"❌ Error: `{str(e)[:100]}`")

    async def _start_task(self, cid, gid, name, source, sm):
        task = DownloadTask(gid=gid, name=name, chat_id=cid, source=source)
        task.status_msg   = sm
        self.tasks[cid]   = task

        await self._safe_edit(
            sm,
            f"✅ **Descargando**\n"
            f"📁 `{name[:50]}`\n\n"
            f"`{Utils.progress_bar(0)}` `0%`\n"
            f"⏳ Iniciando...",
            parse_mode="markdown"
        )

        task.monitor_task = asyncio.create_task(
            self._monitor(cid), name=f"mon_{cid}"
        )

    # ─── MONITOR CON PROGRESO REAL ───────────────────────────────────────────

    async def _monitor(self, cid: int):
        """Monitor con actualización de progreso en tiempo real"""
        if cid not in self.tasks:
            return

        task       = self.tasks[cid]
        last_pct   = -1.0
        last_upd   = 0.0
        stall_secs = 0
        last_done  = 0

        log.info(f"🔍 Monitor iniciado: {task.name} (GID={task.gid})")

        try:
            while True:
                # Timeout global
                if task.is_timed_out():
                    await self._safe_edit(task.status_msg,
                        "⏰ **Timeout** — descarga demasiado lenta")
                    break

                info = self.aria2.get_status(task.gid)
                if info is None:
                    await asyncio.sleep(Config.UPDATE_INTERVAL)
                    continue

                status    = info.get("status", "")
                total     = int(info.get("totalLength",    0))
                completed = int(info.get("completedLength",0))
                speed     = int(info.get("downloadSpeed",  0))

                # ── Completado ──
                if status == "complete":
                    log.info(f"✅ Completo: {task.name}")
                    files = self._collect_files(info)
                    await self._safe_edit(
                        task.status_msg,
                        f"✅ **Descarga completa!**\n"
                        f"📁 `{task.name[:40]}`\n\n"
                        f"`{Utils.progress_bar(100)}` `100%`\n\n"
                        f"⬆️ Preparando subida..."
                    )
                    await self._process_and_upload(cid, files, task.status_msg)
                    return

                # ── Error ──
                if status == "error":
                    ec  = info.get("errorCode", "?")
                    em  = info.get("errorMessage", "Error")
                    log.error(f"aria2 error [{task.gid}]: [{ec}] {em}")
                    await self._safe_edit(task.status_msg,
                        f"❌ **Error de descarga**\n`{em[:100]}`")
                    break

                if status == "removed":
                    await self._safe_edit(task.status_msg, "🗑 **Eliminado**")
                    break

                # ── Progreso ──
                pct = (completed / total * 100) if total > 0 else 0

                # Detectar stall
                if completed > last_done:
                    stall_secs = 0
                    last_done  = completed
                else:
                    stall_secs += Config.UPDATE_INTERVAL

                if stall_secs >= Config.MAX_STALL_TIME and total > 0:
                    await self._safe_edit(task.status_msg,
                        f"⚠️ **Sin progreso por {Utils.format_eta(Config.MAX_STALL_TIME)}**\n"
                        f"Sin seeds. Usa `/cancel` para cancelar.")
                    stall_secs = 0

                # Actualizar nombre real (torrents)
                if task.source in ("magnet","torrent"):
                    bt = info.get("bittorrent",{})
                    rn = bt.get("info",{}).get("name","")
                    if rn and rn != task.name:
                        task.name = rn

                # Actualizar mensaje
                now = time.time()
                pct_changed  = abs(pct - last_pct) >= 0.5
                time_elapsed = (now - last_upd) >= Config.UPDATE_INTERVAL

                if pct_changed or time_elapsed:
                    last_pct = pct
                    last_upd = now
                    text = self._build_progress_text(task, info)
                    await self._safe_edit(task.status_msg, text, parse_mode="markdown")

                await asyncio.sleep(Config.UPDATE_INTERVAL)

        except asyncio.CancelledError:
            log.info(f"Monitor cancelado: CID={cid}")
        except Exception as e:
            log.error(f"Error monitor [{cid}]: {e}", exc_info=True)
            await self._safe_edit(
                self.tasks[cid].status_msg if cid in self.tasks else None,
                f"❌ Error interno: `{str(e)[:100]}`"
            )
        finally:
            if cid in self.tasks:
                del self.tasks[cid]
            log.info(f"🏁 Monitor finalizado: CID={cid}")

    def _build_progress_text(self, task: DownloadTask, info: dict) -> str:
        """Construye texto de progreso con todos los datos"""
        total     = int(info.get("totalLength",    0))
        completed = int(info.get("completedLength",0))
        speed     = int(info.get("downloadSpeed",  0))
        status    = info.get("status","")

        pct = (completed / total * 100) if total > 0 else 0
        bar = Utils.progress_bar(pct)

        eta = Utils.format_eta((total - completed) / speed) if speed > 0 and total > completed else "∞"

        icons = {"active":"⬇️","waiting":"⏳","paused":"⏸️","complete":"✅"}
        icon  = icons.get(status, "🔄")

        # Línea de tamaño
        if total > 0:
            size_line = f"📦 {Utils.format_size(completed)} / {Utils.format_size(total)}"
        else:
            size_line = f"📦 {Utils.format_size(completed)}"

        lines = [
            f"{icon} **{task.name[:40]}**",
            "",
            f"`{bar}` `{pct:.1f}%`",
            f"🚀 {Utils.format_speed(speed)}",
            size_line,
            f"⏱ ETA: {eta}",
            f"⏳ Tiempo: {Utils.format_eta(task.elapsed)}",
        ]

        if status == "waiting":
            lines.append("_(en cola — esperando peers)_")
        if status == "paused":
            lines.append("_(pausado)_")

        return "\n".join(lines)

    # ─── RECOLECCIÓN DE ARCHIVOS ──────────────────────────────────────────────

    def _collect_files(self, info: dict) -> List[str]:
        """Obtiene lista de archivos completados"""
        files = []

        # Desde aria2 info
        for f in info.get("files", []):
            fp  = f.get("path","")
            sel = f.get("selected","true")
            if fp and sel != "false" and os.path.isfile(fp):
                if os.path.getsize(fp) > 0:
                    ext = Path(fp).suffix.lower()
                    if ext not in (".torrent",".aria2",".session",".dat"):
                        files.append(fp)

        # Fallback: buscar en directorio
        if not files:
            skip = {".torrent",".aria2",".session",".dat",".log",".txt"}
            for f in sorted(self.storage.rglob("*")):
                if f.is_file() and f.stat().st_size > 0:
                    if f.suffix.lower() not in skip and "tmp_" not in f.name:
                        files.append(str(f))

        # Ordenar: primero videos, luego por tamaño
        videos    = [f for f in files if Utils.is_video(f)]
        non_video = [f for f in files if not Utils.is_video(f)]
        videos.sort(   key=lambda x: os.path.getsize(x), reverse=True)
        non_video.sort(key=lambda x: os.path.getsize(x), reverse=True)

        return videos + non_video

    # ─── CONVERSIÓN + SUBIDA ─────────────────────────────────────────────────

    async def _process_and_upload(self, cid: int, files: List[str], sm):
        """Convierte si es necesario y sube al canal"""
        if not files:
            await self._safe_edit(sm, "⚠️ **No se encontraron archivos**")
            await self.client.send_message(cid, "⚠️ Sin archivos para subir", parse_mode="markdown")
            return

        uploaded = 0
        failed   = 0
        skipped  = 0

        for fp in files:
            if not os.path.isfile(fp):
                continue

            fn   = os.path.basename(fp)
            size = os.path.getsize(fp)

            # Tamaño máximo
            if size > Config.MAX_FILE_SIZE:
                log.warning(f"Omitido (muy grande): {fn} {Utils.format_size(size)}")
                await self.client.send_message(
                    cid,
                    f"⚠️ `{fn}` ({Utils.format_size(size)}) supera el límite de Telegram (2GB)",
                    parse_mode="markdown"
                )
                skipped += 1
                continue

            # ── Conversión de video ──
            if Utils.is_video(fp) and VideoConverter.ffmpeg_available():
                suffix = Path(fp).suffix.lower()
                needs  = Utils.needs_conversion(fp)

                # Para MP4 verificar si tiene codec compatible
                if suffix == ".mp4":
                    vinfo = VideoConverter.get_video_info(fp)
                    vc    = vinfo.get("vcodec","")
                    ac    = vinfo.get("acodec","")
                    needs = vc not in ("h264","avc","avc1") or ac not in ("aac","mp4a")
                    if needs:
                        log.info(f"MP4 con codec {vc}/{ac} — necesita conversión")

                if needs:
                    await self._safe_edit(
                        sm,
                        f"🎬 **Convirtiendo video...**\n"
                        f"`{fn[:50]}`\n\n"
                        f"`{Utils.progress_bar(0)}` `0%`\n"
                        f"⚙️ Preparando ffmpeg...",
                        parse_mode="markdown"
                    )

                    async def conv_progress(pct, eta_secs):
                        bar = Utils.progress_bar(pct)
                        await self._safe_edit(
                            sm,
                            f"🎬 **Convirtiendo video...**\n"
                            f"`{fn[:50]}`\n\n"
                            f"`{bar}` `{pct:.1f}%`\n"
                            f"⏱ ETA: {Utils.format_eta(eta_secs)}",
                            parse_mode="markdown"
                        )

                    converted = await VideoConverter.convert(fp, conv_progress)

                    if converted:
                        fp   = converted
                        fn   = os.path.basename(fp)
                        size = os.path.getsize(fp)
                        log.info(f"✅ Convertido: {fn}")
                    else:
                        log.warning(f"Conversión falló, subiendo original: {fn}")
                        await self._safe_edit(
                            sm,
                            f"⚠️ Conversión falló, subiendo original...\n`{fn}`"
                        )
                else:
                    log.info(f"✅ {fn} ya es compatible con Telegram")

            # ── Subir a Telegram ──
            fn   = os.path.basename(fp)
            size = os.path.getsize(fp)
            is_v = Utils.is_video(fp)

            log.info(f"📤 Subiendo: {fn} ({Utils.format_size(size)})")

            # Estado inicial de subida
            await self._safe_edit(
                sm,
                f"📤 **Subiendo al canal...**\n"
                f"`{fn[:50]}`\n\n"
                f"`{Utils.progress_bar(0)}` `0%`\n"
                f"📦 {Utils.format_size(size)}",
                parse_mode="markdown"
            )

            try:
                last_edit_time = [0.0]

                async def upload_progress(sent, total):
                    """Callback de progreso de subida"""
                    now = time.time()
                    if now - last_edit_time[0] < 3:   # máx 1 update cada 3s
                        return
                    last_edit_time[0] = now

                    pct  = (sent / total * 100) if total > 0 else 0
                    bar  = Utils.progress_bar(pct)
                    await self._safe_edit(
                        sm,
                        f"📤 **Subiendo al canal...**\n"
                        f"`{fn[:50]}`\n\n"
                        f"`{bar}` `{pct:.1f}%`\n"
                        f"📦 {Utils.format_size(sent)} / {Utils.format_size(total)}",
                        parse_mode="markdown"
                    )

                # Thumbnail para videos
                thumb = None
                vinfo = {}
                if is_v:
                    thumb = VideoConverter.generate_thumbnail(fp)
                    vinfo = VideoConverter.get_video_info(fp)

                # Atributos de video para Telegram
                attributes = []
                if is_v and vinfo.get("duration"):
                    attributes.append(DocumentAttributeVideo(
                        duration  = vinfo["duration"],
                        w         = vinfo.get("width",  0),
                        h         = vinfo.get("height", 0),
                        supports_streaming = True,
                    ))

                # Enviar archivo
                caption = (
                    f"🎬 **{fn}**\n💾 {Utils.format_size(size)}"
                    if is_v else
                    f"📁 **{fn}**\n💾 {Utils.format_size(size)}"
                )

                await self.client.send_file(
                    Config.CHANNEL_ID,
                    file              = fp,
                    caption           = caption,
                    parse_mode        = "markdown",
                    force_document    = False if is_v else True,
                    thumb             = thumb,
                    attributes        = attributes if attributes else None,
                    progress_callback = upload_progress,
                )

                # Limpiar thumbnail
                if thumb and os.path.exists(thumb):
                    try: os.remove(thumb)
                    except: pass

                log.info(f"✅ Subido: {fn}")
                uploaded += 1

                # Eliminar archivo local
                try:
                    os.remove(fp)
                    log.info(f"🗑 Eliminado: {fn}")
                except Exception as e:
                    log.warning(f"No se pudo eliminar {fn}: {e}")

                await asyncio.sleep(1)

            except FloodWaitError as e:
                log.warning(f"FloodWait {e.seconds}s")
                await asyncio.sleep(e.seconds + 5)
                try:
                    await self.client.send_file(
                        Config.CHANNEL_ID, file=fp,
                        caption=f"📁 {fn}", force_document=True
                    )
                    uploaded += 1
                except Exception as e2:
                    log.error(f"Reintento fallido {fn}: {e2}")
                    failed += 1

            except ChatWriteForbiddenError:
                log.error("Sin permisos en el canal")
                await self.client.send_message(
                    cid,
                    "❌ **El bot no tiene permisos para enviar al canal**\n"
                    "Agrégalo como administrador con permisos para publicar.",
                    parse_mode="markdown"
                )
                break

            except Exception as e:
                log.error(f"Error subiendo {fn}: {e}", exc_info=True)
                failed += 1

        # ── Resumen final ──
        if uploaded > 0 and failed == 0:
            summary = f"✅ **¡Listo!** {uploaded} archivo(s) enviado(s) al canal 🎉"
        elif uploaded > 0 and failed > 0:
            summary = f"⚠️ **Parcial:** ✅{uploaded} subido(s) | ❌{failed} fallido(s)"
        elif skipped > 0:
            summary = f"⚠️ **Omitidos:** {skipped} (muy grandes para Telegram)"
        else:
            summary = "❌ **No se pudo subir ningún archivo**"

        await self.client.send_message(cid, summary, parse_mode="markdown")
        await self._safe_delete(sm)

    # ─── HELPERS ─────────────────────────────────────────────────────────────

    async def _cancel_task(self, cid: int):
        if cid not in self.tasks:
            return
        task = self.tasks[cid]

        if task.monitor_task and not task.monitor_task.done():
            task.monitor_task.cancel()
            try:
                await asyncio.wait_for(asyncio.shield(task.monitor_task), timeout=3)
            except:
                pass

        self.aria2.remove(task.gid)
        await self._safe_delete(task.status_msg)
        del self.tasks[cid]
        log.info(f"✓ Cancelado: CID={cid}")

    @staticmethod
    async def _safe_edit(msg, text: str, parse_mode: str = "markdown"):
        if not msg:
            return
        try:
            await msg.edit(text, parse_mode=parse_mode)
        except (MessageNotModifiedError, MessageIdInvalidError):
            pass
        except FloodWaitError as e:
            await asyncio.sleep(e.seconds + 1)
        except Exception as e:
            log.debug(f"_safe_edit: {e}")

    @staticmethod
    async def _safe_delete(msg):
        if not msg:
            return
        try:
            await msg.delete()
        except:
            pass

    # ─── CIERRE ──────────────────────────────────────────────────────────────

    async def stop(self):
        log.info("🛑 Cerrando...")
        for cid in list(self.tasks.keys()):
            await self._cancel_task(cid)
        try:
            await self.client.disconnect()
        except:
            pass
        Aria2Manager.stop()
        # Limpiar temporales
        for f in self.storage.glob("tmp_*"):
            try: f.unlink()
            except: pass
        log.info("✓ Bot cerrado")

# ═══════════════════════════════════════════════════════════════════════════════
# MAIN
# ═══════════════════════════════════════════════════════════════════════════════
async def main():
    bot  = TeleTorrentBot()
    loop = asyncio.get_event_loop()

    def on_signal(sig):
        log.info(f"Señal: {sig.name}")
        asyncio.create_task(bot.stop())

    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, lambda s=sig: on_signal(s))
        except NotImplementedError:
            pass

    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Ctrl+C")
    except Exception as e:
        log.error(f"Error fatal: {e}", exc_info=True)
    finally:
        await bot.stop()


if __name__ == "__main__":
    asyncio.run(main())
