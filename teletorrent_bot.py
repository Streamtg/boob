#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v13.0 - VERSIÓN ROBUSTA Y DEFINITIVA
Descarga magnet links, torrents y URLs HTTP/HTTPS
Sube archivos a Telegram automáticamente
A prueba de fallos con manejo exhaustivo de errores
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
import subprocess
import tempfile
import time
from pathlib import Path
from typing import Optional, Dict, List, Tuple
from urllib.parse import unquote, urlparse

import requests
from telethon import TelegramClient, events
from telethon.errors import (
    FloodWaitError,
    MessageNotModifiedError,
    MessageIdInvalidError,
    ChatWriteForbiddenError,
)
from telethon.tl.types import DocumentAttributeFilename

# ═══════════════════════════════════════════════════════════════════════════════
# CONFIGURACIÓN
# ═══════════════════════════════════════════════════════════════════════════════
class Config:
    API_ID          = 34280578
    API_HASH        = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN       = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID      = -1003213143951
    STORAGE_PATH    = "./downloads"
    ARIA2_PORT      = 6800
    ARIA2_SECRET    = ""                  # Opcional: token RPC
    MAX_RETRIES     = 5
    UPDATE_INTERVAL = 5                   # Segundos entre actualizaciones
    MAX_FILE_SIZE   = 2 * 1024**3         # 2 GB límite Telegram
    ARIA2_TIMEOUT   = 30                  # Timeout para operaciones aria2
    MONITOR_TIMEOUT = 7200               # 2 horas máximo de descarga

# ═══════════════════════════════════════════════════════════════════════════════
# LOGGING
# ═══════════════════════════════════════════════════════════════════════════════
def setup_logging() -> logging.Logger:
    fmt = logging.Formatter(
        "%(asctime)s [%(levelname)s] %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S"
    )
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(fmt)

    logger = logging.getLogger("TeleTorrent")
    logger.setLevel(logging.INFO)
    logger.addHandler(handler)

    # Log a archivo también
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
        """Formatea bytes a unidad legible"""
        try:
            n = float(n) if n else 0.0
            if n < 0:
                n = 0.0
        except (TypeError, ValueError):
            n = 0.0

        for unit in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024.0:
                return f"{n:.2f} {unit}"
            n /= 1024.0
        return f"{n:.2f} PB"

    @staticmethod
    def format_speed(bps) -> str:
        """Formatea velocidad"""
        return f"{Utils.format_size(bps)}/s"

    @staticmethod
    def format_eta(seconds) -> str:
        """Formatea tiempo restante"""
        try:
            seconds = int(seconds)
            if seconds <= 0 or seconds > 86400 * 7:
                return "∞"
            h, rem = divmod(seconds, 3600)
            m, s   = divmod(rem, 60)
            if h > 0:
                return f"{h}h {m}m"
            if m > 0:
                return f"{m}m {s}s"
            return f"{s}s"
        except:
            return "∞"

    @staticmethod
    def progress_bar(p: float, width: int = 20) -> str:
        """Genera barra de progreso"""
        try:
            p = min(100.0, max(0.0, float(p)))
        except:
            p = 0.0
        filled = int(p / (100 / width))
        return "█" * filled + "░" * (width - filled)

    @staticmethod
    def sanitize_filename(name: str) -> str:
        """Limpia nombre de archivo"""
        name = re.sub(r'[<>:"/\\|?*\x00-\x1f]', "_", name)
        name = name.strip(". ")
        return name[:200] or "descarga"

    @staticmethod
    def get_file_md5(fp: str) -> Optional[str]:
        """Calcula MD5 de archivo"""
        try:
            h = hashlib.md5()
            with open(fp, "rb") as f:
                for chunk in iter(lambda: f.read(8192), b""):
                    h.update(chunk)
            return h.hexdigest()
        except:
            return None

    @staticmethod
    def is_torrent_file(data: bytes) -> bool:
        """Verifica si los bytes son un archivo .torrent válido"""
        return data[:2] in (b"d8", b"d6") or data[:1] == b"d"

    @staticmethod
    def extract_name_from_magnet(magnet: str) -> str:
        """Extrae nombre del magnet link"""
        dn = re.search(r"[?&]dn=([^&]+)", magnet)
        if dn:
            try:
                return unquote(dn.group(1)).replace("+", " ")
            except:
                pass
        ih = re.search(r"xt=urn:btih:([a-fA-F0-9]{40}|[A-Z2-7]{32})", magnet)
        if ih:
            return f"Torrent_{ih.group(1)[:8]}"
        return "Descarga_Magnet"

# ═══════════════════════════════════════════════════════════════════════════════
# ARIA2 RPC CLIENT - ROBUSTO
# ═══════════════════════════════════════════════════════════════════════════════
class Aria2RPC:
    """Cliente JSON-RPC para aria2 con reconexión automática"""

    def __init__(self, port: int = Config.ARIA2_PORT, secret: str = Config.ARIA2_SECRET):
        self.port    = port
        self.secret  = secret
        self.url     = f"http://127.0.0.1:{port}/jsonrpc"
        self._req_id = 0
        self.ready   = False
        self.session = requests.Session()
        self.session.headers.update({"Content-Type": "application/json"})

    def wait_ready(self, max_attempts: int = 40) -> bool:
        """Espera a que aria2 RPC esté disponible"""
        log.info("⏳ Esperando aria2 RPC...")
        for i in range(max_attempts):
            try:
                r = self.session.post(
                    self.url,
                    json={"jsonrpc": "2.0", "id": 0, "method": "aria2.getVersion", "params": []},
                    timeout=3
                )
                if r.status_code == 200:
                    data = r.json()
                    if "result" in data:
                        ver = data["result"].get("version", "?")
                        log.info(f"✓ aria2 RPC listo (v{ver})")
                        self.ready = True
                        return True
            except Exception:
                pass
            time.sleep(1)
            if i % 5 == 4:
                log.info(f"  ...intentando ({i+1}/{max_attempts})")

        log.error("❌ aria2 RPC no responde")
        return False

    def _build_params(self, params: list) -> list:
        """Agrega token de autenticación si está configurado"""
        if self.secret:
            return [f"token:{self.secret}"] + params
        return params

    def _call(self, method: str, params: list = None, retries: int = 3) -> Optional[any]:
        """Llamada JSON-RPC con reintentos"""
        params = params or []
        full_params = self._build_params(params)

        for attempt in range(retries):
            try:
                self._req_id += 1
                payload = {
                    "jsonrpc": "2.0",
                    "id":      self._req_id,
                    "method":  method,
                    "params":  full_params
                }

                r = self.session.post(self.url, json=payload, timeout=Config.ARIA2_TIMEOUT)

                if r.status_code != 200:
                    log.warning(f"aria2 HTTP {r.status_code}")
                    continue

                data = r.json()

                if "error" in data:
                    err = data["error"]
                    log.warning(f"aria2 error [{method}]: {err.get('message', err)}")
                    return None

                return data.get("result")

            except requests.exceptions.ConnectionError:
                if attempt < retries - 1:
                    log.warning(f"aria2 desconectado, reintento {attempt+1}/{retries}...")
                    time.sleep(2)
                    self.ready = False
                    self.wait_ready(10)
                else:
                    log.error("aria2 RPC no disponible")
            except requests.exceptions.Timeout:
                log.warning(f"aria2 timeout [{method}]")
            except Exception as e:
                log.warning(f"aria2 excepción [{method}]: {e}")

        return None

    def add_uri(self, uris: List[str], options: dict = None) -> Optional[str]:
        """Agrega URIs (magnet, http, etc.). Retorna GID"""
        params = [uris]
        if options:
            params.append({k: str(v) for k, v in options.items()})
        result = self._call("aria2.addUri", params)
        if isinstance(result, str) and result:
            log.info(f"✓ aria2 GID={result}")
            return result
        log.error(f"add_uri falló: {result}")
        return None

    def add_torrent(self, torrent_b64: str, options: dict = None) -> Optional[str]:
        """Agrega archivo .torrent en base64. Retorna GID"""
        import base64
        params = [torrent_b64, [], options or {}]
        result = self._call("aria2.addTorrent", params)
        if isinstance(result, str) and result:
            log.info(f"✓ aria2 torrent GID={result}")
            return result
        return None

    def get_status(self, gid: str) -> Optional[dict]:
        """Estado de una descarga"""
        keys = [
            "gid", "status", "totalLength", "completedLength",
            "downloadSpeed", "uploadSpeed", "files", "bittorrent",
            "errorCode", "errorMessage", "followedBy"
        ]
        result = self._call("aria2.tellStatus", [gid, keys])
        return result if isinstance(result, dict) else None

    def remove(self, gid: str) -> bool:
        """Cancela y elimina descarga"""
        # Intentar pausa primero, luego remove
        self._call("aria2.pause", [gid])
        time.sleep(0.5)
        r = self._call("aria2.remove", [gid])
        if r is None:
            r = self._call("aria2.forceRemove", [gid])
        return r is not None

    def purge(self, gid: str):
        """Elimina del historial"""
        self._call("aria2.removeDownloadResult", [gid])

    def get_version(self) -> Optional[str]:
        """Obtiene versión de aria2"""
        r = self._call("aria2.getVersion")
        return r.get("version") if isinstance(r, dict) else None

# ═══════════════════════════════════════════════════════════════════════════════
# GESTOR ARIA2C
# ═══════════════════════════════════════════════════════════════════════════════
class Aria2Manager:
    """Gestiona el proceso aria2c"""

    @staticmethod
    def kill_existing():
        """Mata cualquier proceso aria2c existente"""
        try:
            result = subprocess.run(
                ["pgrep", "-f", "aria2c"],
                capture_output=True, text=True, timeout=5
            )
            if result.stdout.strip():
                subprocess.run(["pkill", "-9", "-f", "aria2c"], timeout=5)
                time.sleep(1.5)
                log.info("✓ Procesos aria2c anteriores eliminados")
        except Exception:
            pass

    @staticmethod
    def check_installed() -> bool:
        """Verifica que aria2c esté instalado"""
        return shutil.which("aria2c") is not None

    @staticmethod
    def is_running() -> bool:
        """Verifica si aria2c está corriendo"""
        try:
            r = subprocess.run(["pgrep", "-f", "aria2c"], capture_output=True, timeout=5)
            return r.returncode == 0
        except:
            return False

    @staticmethod
    def start(storage_path: str) -> bool:
        """Inicia aria2c daemon con configuración óptima"""
        if not Aria2Manager.check_installed():
            log.error("❌ aria2c no instalado. Ejecuta: sudo apt-get install -y aria2")
            return False

        Aria2Manager.kill_existing()

        sp = Path(storage_path).absolute()
        sp.mkdir(parents=True, exist_ok=True)

        session_file = sp / "aria2_session.dat"
        log_file     = sp / "aria2_daemon.log"

        # Configuración aria2c completa y robusta
        cmd = [
            "aria2c",
            # RPC
            "--enable-rpc=true",
            "--rpc-listen-all=true",
            f"--rpc-listen-port={Config.ARIA2_PORT}",
            "--rpc-allow-origin-all=true",
            "--rpc-save-upload-metadata=true",
            # Directorio
            f"--dir={sp}",
            # Conexiones
            "--max-concurrent-downloads=5",
            "--max-connection-per-server=8",
            "--min-split-size=5M",
            "--split=8",
            "--max-tries=10",
            "--retry-wait=5",
            "--connect-timeout=30",
            "--timeout=60",
            # Rendimiento
            "--continue=true",
            "--allow-overwrite=true",
            "--auto-file-renaming=false",
            "--disk-cache=64M",
            "--file-allocation=none",           # más compatible
            # BitTorrent
            "--enable-dht=true",
            "--enable-dht6=false",
            "--enable-peer-exchange=true",
            "--bt-enable-lpd=true",
            "--bt-max-peers=100",
            "--bt-request-peer-speed-limit=10M",
            "--seed-time=0",                    # No seedear
            "--bt-stop-timeout=300",            # 5 min sin peers = parar
            "--follow-torrent=mem",
            # Sesión
            f"--save-session={session_file}",
            "--save-session-interval=30",
            # Daemon y logs
            "--daemon=true",
            "--quiet=false",
            f"--log={log_file}",
            "--log-level=notice",
        ]

        # Agregar secret si está configurado
        if Config.ARIA2_SECRET:
            cmd.append(f"--rpc-secret={Config.ARIA2_SECRET}")

        # Cargar sesión anterior si existe
        if session_file.exists() and session_file.stat().st_size > 0:
            cmd.append(f"--input-file={session_file}")

        log.info("🚀 Iniciando aria2c daemon...")

        try:
            proc = subprocess.Popen(
                cmd,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.PIPE,
                start_new_session=True,
                text=True
            )

            # Esperar inicio
            time.sleep(2)

            if not Aria2Manager.is_running():
                stderr = proc.stderr.read() if proc.stderr else ""
                log.error(f"❌ aria2c falló: {stderr[:300]}")
                return False

            log.info(f"✓ aria2c iniciado correctamente")
            return True

        except FileNotFoundError:
            log.error("❌ aria2c no encontrado")
            return False
        except Exception as e:
            log.error(f"❌ Error iniciando aria2c: {e}")
            return False

    @staticmethod
    def stop():
        """Detiene aria2c limpiamente"""
        try:
            subprocess.run(["pkill", "-SIGTERM", "-f", "aria2c"], timeout=5)
            time.sleep(2)
            if Aria2Manager.is_running():
                subprocess.run(["pkill", "-9", "-f", "aria2c"], timeout=5)
            log.info("✓ aria2c detenido")
        except:
            pass

# ═══════════════════════════════════════════════════════════════════════════════
# CACHÉ DE ARCHIVOS
# ═══════════════════════════════════════════════════════════════════════════════
class FileCache:
    """Caché persistente de file_ids de Telegram"""

    def __init__(self, cache_file: Path):
        self.cache_file = cache_file
        self._data: dict = self._load()

    def _load(self) -> dict:
        if self.cache_file.exists():
            try:
                return json.loads(self.cache_file.read_text(encoding="utf-8"))
            except:
                pass
        return {}

    def save(self):
        try:
            self.cache_file.write_text(
                json.dumps(self._data, indent=2, ensure_ascii=False),
                encoding="utf-8"
            )
        except Exception as e:
            log.warning(f"Error guardando caché: {e}")

    def get_file_id(self, md5: str) -> Optional[str]:
        entry = self._data.get(md5)
        if entry and isinstance(entry, dict):
            return entry.get("file_id")
        return None

    def set_file_id(self, md5: str, file_id: str, filename: str):
        self._data[md5] = {
            "file_id":   file_id,
            "filename":  filename,
            "timestamp": time.time()
        }
        self.save()

    def cleanup_old(self, days: int = 30):
        """Limpia entradas viejas"""
        cutoff = time.time() - (days * 86400)
        old_keys = [k for k, v in self._data.items()
                    if isinstance(v, dict) and v.get("timestamp", 0) < cutoff]
        for k in old_keys:
            del self._data[k]
        if old_keys:
            self.save()

# ═══════════════════════════════════════════════════════════════════════════════
# TAREA DE DESCARGA
# ═══════════════════════════════════════════════════════════════════════════════
class DownloadTask:
    """Representa una tarea de descarga activa"""

    def __init__(self, gid: str, name: str, chat_id: int, source: str = ""):
        self.gid          = gid
        self.name         = name
        self.chat_id      = chat_id
        self.source       = source
        self.started_at   = time.time()
        self.status_msg   = None          # Mensaje de Telegram para actualizar
        self.monitor_task = None          # asyncio.Task del monitor
        self.files        = []            # Archivos descargados

    @property
    def elapsed(self) -> float:
        return time.time() - self.started_at

    def is_timed_out(self) -> bool:
        return self.elapsed > Config.MONITOR_TIMEOUT

# ═══════════════════════════════════════════════════════════════════════════════
# BOT PRINCIPAL
# ═══════════════════════════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot Telegram robusto para descarga de torrents y archivos"""

    def __init__(self):
        self.storage_path = Path(Config.STORAGE_PATH).absolute()
        self.storage_path.mkdir(parents=True, exist_ok=True)

        self.cache       = FileCache(self.storage_path / "cache.json")
        self.aria2: Optional[Aria2RPC] = None
        self.tasks: Dict[int, DownloadTask] = {}   # chat_id → DownloadTask

        self.client = TelegramClient(
            str(self.storage_path / "teletorrent_session"),
            Config.API_ID,
            Config.API_HASH,
            connection_retries=10,
            retry_delay=5,
            timeout=30,
            request_retries=5,
        )

        log.info("✓ TeleTorrentBot inicializado")

    # ─── INICIO ──────────────────────────────────────────────────────────────

    async def start(self):
        """Inicia el bot completo"""
        # 1. Iniciar aria2c
        if not Aria2Manager.start(str(self.storage_path)):
            log.error("❌ No se pudo iniciar aria2c")
            sys.exit(1)

        # 2. Conectar RPC
        self.aria2 = Aria2RPC()
        if not self.aria2.wait_ready():
            log.error("❌ aria2 RPC no disponible")
            Aria2Manager.stop()
            sys.exit(1)

        # 3. Limpiar caché viejo
        self.cache.cleanup_old()

        # 4. Conectar Telegram
        log.info("🔌 Conectando a Telegram...")
        await self.client.start(bot_token=Config.BOT_TOKEN)

        me = await self.client.get_me()
        log.info(f"✓ Bot: @{me.username} (ID: {me.id})")

        # Verificar canal
        try:
            ch = await self.client.get_entity(Config.CHANNEL_ID)
            log.info(f"✓ Canal configurado: {ch.title}")
        except Exception as e:
            log.warning(f"⚠️ Canal no accesible: {e} — las subidas pueden fallar")

        # 5. Registrar handlers
        self._register_handlers()

        log.info("✅ Bot listo — esperando mensajes...")
        await self.client.run_until_disconnected()

    # ─── HANDLERS ────────────────────────────────────────────────────────────

    def _register_handlers(self):
        """Registra todos los handlers de eventos"""

        # ── /start y /help ──
        @self.client.on(events.NewMessage(pattern=r"^/(?:start|help)$"))
        async def cmd_help(event):
            await event.reply(
                "🚀 **TeleTorrent Bot v13.0**\n\n"
                "Descarga torrents y archivos → sube a Telegram\n\n"
                "**📋 Cómo usar:**\n"
                "• Pega un **magnet link**\n"
                "• Pega una **URL HTTP/HTTPS**\n"
                "• Envía un **archivo .torrent**\n\n"
                "**⚙️ Comandos:**\n"
                "`/status` — ver progreso actual\n"
                "`/cancel` — cancelar descarga\n"
                "`/storage` — ver espacio libre\n"
                "`/help` — esta ayuda\n\n"
                "**📦 Límite:** 2 GB por archivo",
                parse_mode="markdown"
            )

        # ── /status ──
        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def cmd_status(event):
            cid = event.chat_id

            if cid not in self.tasks:
                await event.reply("⏸ **Sin descargas activas**", parse_mode="markdown")
                return

            task = self.tasks[cid]
            info = self.aria2.get_status(task.gid)

            if not info:
                await event.reply("⚠️ **No se pudo obtener estado**", parse_mode="markdown")
                return

            text = self._format_status(task, info)
            await event.reply(text, parse_mode="markdown")

        # ── /cancel ──
        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cmd_cancel(event):
            cid = event.chat_id

            if cid not in self.tasks:
                await event.reply("ℹ️ **Nada que cancelar**", parse_mode="markdown")
                return

            await self._cancel_task(cid, notify=True)
            await event.reply("✅ **Descarga cancelada**", parse_mode="markdown")

        # ── /storage ──
        @self.client.on(events.NewMessage(pattern=r"^/storage$"))
        async def cmd_storage(event):
            try:
                stat  = shutil.disk_usage(self.storage_path)
                files = list(self.storage_path.rglob("*"))
                files = [f for f in files if f.is_file()]
                total_files_size = sum(f.stat().st_size for f in files)

                await event.reply(
                    f"💾 **Almacenamiento**\n\n"
                    f"Total disco: `{Utils.format_size(stat.total)}`\n"
                    f"Usado: `{Utils.format_size(stat.used)}`\n"
                    f"Libre: `{Utils.format_size(stat.free)}`\n"
                    f"Archivos en descarga: `{len(files)}`\n"
                    f"Tamaño total: `{Utils.format_size(total_files_size)}`",
                    parse_mode="markdown"
                )
            except Exception as e:
                await event.reply(f"❌ Error: {e}")

        # ── MENSAJES GENERALES ──
        @self.client.on(events.NewMessage)
        async def cmd_message(event):
            # Ignorar comandos (ya manejados arriba)
            text = (event.message.text or "").strip()
            if text.startswith("/"):
                return

            # Magnet link
            if text.lower().startswith("magnet:?xt="):
                await self._handle_magnet(event, text)
                return

            # URL HTTP/HTTPS
            if re.match(r"https?://", text, re.IGNORECASE):
                await self._handle_url(event, text)
                return

            # Archivo adjunto (documento)
            if event.message.document:
                await self._handle_document(event)
                return

    # ─── MANEJADORES DE TIPO ─────────────────────────────────────────────────

    async def _handle_magnet(self, event, magnet: str):
        """Procesa magnet link"""
        cid = event.chat_id

        if not self._check_no_active(cid):
            await event.reply(
                "⚠️ **Ya hay una descarga activa**\n"
                "Usa `/cancel` para cancelarla primero.",
                parse_mode="markdown"
            )
            return

        # Validar magnet
        if not re.search(r"xt=urn:btih:", magnet, re.IGNORECASE):
            await event.reply("❌ **Magnet link inválido**", parse_mode="markdown")
            return

        name = Utils.extract_name_from_magnet(magnet)
        sm   = await event.reply(
            f"⏳ **Procesando magnet...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        try:
            options = {
                "dir":          str(self.storage_path),
                "seed-time":    "0",
                "bt-stop-timeout": "300",
            }

            gid = self.aria2.add_uri([magnet], options)

            if not gid:
                await self._safe_edit(sm, "❌ **No se pudo agregar el magnet**\naria2 no respondió correctamente.")
                return

            task = DownloadTask(gid=gid, name=name, chat_id=cid, source="magnet")
            task.status_msg = sm
            self.tasks[cid]  = task

            await self._safe_edit(
                sm,
                f"✅ **Magnet agregado**\n"
                f"📁 `{name[:50]}`\n\n"
                f"`{Utils.progress_bar(0)}` `0%`\n"
                f"⏳ Conectando a peers...",
                parse_mode="markdown"
            )

            task.monitor_task = asyncio.create_task(
                self._monitor_download(cid),
                name=f"monitor_{cid}"
            )

        except Exception as e:
            log.error(f"Error en magnet [{cid}]: {e}", exc_info=True)
            await self._safe_edit(sm, f"❌ **Error:** `{str(e)[:100]}`")

    async def _handle_url(self, event, url: str):
        """Procesa URL HTTP/HTTPS"""
        cid = event.chat_id

        if not self._check_no_active(cid):
            await event.reply(
                "⚠️ **Ya hay una descarga activa**\n"
                "Usa `/cancel` para cancelarla primero.",
                parse_mode="markdown"
            )
            return

        # Validar URL básica
        try:
            parsed = urlparse(url)
            if not parsed.scheme or not parsed.netloc:
                raise ValueError("URL malformada")
        except Exception:
            await event.reply("❌ **URL inválida**", parse_mode="markdown")
            return

        # Nombre de archivo desde URL
        path_part = parsed.path.rstrip("/")
        raw_name  = unquote(path_part.split("/")[-1]) if path_part else ""
        name      = Utils.sanitize_filename(raw_name) if raw_name else "descarga"

        sm = await event.reply(
            f"⏳ **Iniciando descarga...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        try:
            options = {
                "dir":                   str(self.storage_path),
                "out":                   name,
                "max-tries":             "10",
                "retry-wait":            "5",
                "connect-timeout":       "30",
                "timeout":               "60",
                "split":                 "8",
                "max-connection-per-server": "8",
                "min-split-size":        "5M",
            }

            gid = self.aria2.add_uri([url], options)

            if not gid:
                await self._safe_edit(sm, "❌ **No se pudo agregar la URL**\nVerifica que sea válida.")
                return

            task = DownloadTask(gid=gid, name=name, chat_id=cid, source="url")
            task.status_msg = sm
            self.tasks[cid]  = task

            await self._safe_edit(
                sm,
                f"✅ **Descargando**\n"
                f"📄 `{name[:50]}`\n\n"
                f"`{Utils.progress_bar(0)}` `0%`\n"
                f"⏳ Iniciando...",
                parse_mode="markdown"
            )

            task.monitor_task = asyncio.create_task(
                self._monitor_download(cid),
                name=f"monitor_{cid}"
            )

        except Exception as e:
            log.error(f"Error en URL [{cid}]: {e}", exc_info=True)
            await self._safe_edit(sm, f"❌ **Error:** `{str(e)[:100]}`")

    async def _handle_document(self, event):
        """Procesa archivo .torrent enviado al bot"""
        cid = event.chat_id
        doc = event.message.document

        if not self._check_no_active(cid):
            await event.reply(
                "⚠️ **Ya hay una descarga activa**\n"
                "Usa `/cancel` para cancelarla primero.",
                parse_mode="markdown"
            )
            return

        # Obtener nombre del archivo
        name = "archivo_desconocido"
        if doc.attributes:
            for attr in doc.attributes:
                if isinstance(attr, DocumentAttributeFilename):
                    name = attr.file_name
                    break

        is_torrent = name.lower().endswith(".torrent") or doc.mime_type == "application/x-bittorrent"

        sm = await event.reply(
            f"⏳ **Descargando archivo...**\n`{name[:50]}`",
            parse_mode="markdown"
        )

        try:
            # Descargar el documento a un archivo temporal
            tmp_path = self.storage_path / f"tmp_{int(time.time())}_{name}"

            await self._safe_edit(sm, f"📥 **Descargando de Telegram...**\n`{name[:50]}`")

            await event.message.download_media(str(tmp_path))

            if not tmp_path.exists() or tmp_path.stat().st_size == 0:
                await self._safe_edit(sm, "❌ **Archivo vacío o no descargado**")
                return

            if is_torrent:
                # Procesar como torrent
                await self._process_torrent_file(cid, sm, tmp_path, name)
            else:
                # Subir directamente al canal
                await self._safe_edit(sm, f"✅ **Descargado!** Subiendo al canal...\n`{name[:50]}`")
                await self._upload_files(cid, [str(tmp_path)])
                await self._safe_delete(sm)

        except Exception as e:
            log.error(f"Error en documento [{cid}]: {e}", exc_info=True)
            await self._safe_edit(sm, f"❌ **Error:** `{str(e)[:100]}`")

    async def _process_torrent_file(self, cid: int, sm, torrent_path: Path, name: str):
        """Agrega archivo .torrent a aria2"""
        try:
            import base64

            torrent_data = torrent_path.read_bytes()
            torrent_b64  = base64.b64encode(torrent_data).decode("utf-8")

            options = {
                "dir":               str(self.storage_path),
                "seed-time":         "0",
                "bt-stop-timeout":   "300",
            }

            gid = self.aria2.add_torrent(torrent_b64, options)

            # Limpiar archivo temporal
            try:
                torrent_path.unlink()
            except:
                pass

            if not gid:
                await self._safe_edit(sm, "❌ **No se pudo procesar el torrent**")
                return

            display_name = name.replace(".torrent", "")
            task = DownloadTask(gid=gid, name=display_name, chat_id=cid, source="torrent")
            task.status_msg = sm
            self.tasks[cid]  = task

            await self._safe_edit(
                sm,
                f"✅ **Torrent agregado**\n"
                f"📁 `{display_name[:50]}`\n\n"
                f"`{Utils.progress_bar(0)}` `0%`\n"
                f"⏳ Conectando a peers...",
                parse_mode="markdown"
            )

            task.monitor_task = asyncio.create_task(
                self._monitor_download(cid),
                name=f"monitor_{cid}"
            )

        except Exception as e:
            log.error(f"Error procesando torrent: {e}", exc_info=True)
            await self._safe_edit(sm, f"❌ **Error en torrent:** `{str(e)[:100]}`")

    # ─── MONITOR ─────────────────────────────────────────────────────────────

    async def _monitor_download(self, cid: int):
        """Monitorea el progreso de una descarga y sube al terminar"""
        if cid not in self.tasks:
            return

        task = self.tasks[cid]
        log.info(f"🔍 Monitor iniciado: GID={task.gid} ({task.name})")

        last_update_time  = 0
        last_progress_pct = -1
        stall_time        = 0
        last_completed    = 0
        MAX_STALL         = 600  # 10 min sin progreso = error

        try:
            while True:
                # Verificar timeout global
                if task.is_timed_out():
                    log.warning(f"Timeout de descarga: {task.name}")
                    await self._safe_edit(
                        task.status_msg,
                        "⏰ **Timeout** — La descarga tardó demasiado"
                    )
                    break

                # Obtener estado
                info = self.aria2.get_status(task.gid)

                if info is None:
                    log.warning(f"No se pudo obtener estado para GID={task.gid}")
                    await asyncio.sleep(Config.UPDATE_INTERVAL)
                    continue

                status = info.get("status", "")

                # ── Estado: complete ──
                if status == "complete":
                    log.info(f"✅ Descarga completada: {task.name}")
                    await self._safe_edit(
                        task.status_msg,
                        f"✅ **Descarga completa!**\n"
                        f"📁 `{task.name[:50]}`\n\n"
                        f"`{Utils.progress_bar(100)}` `100%`\n\n"
                        f"⬆️ Subiendo al canal...",
                        parse_mode="markdown"
                    )
                    await self._collect_and_upload(cid, info)
                    return

                # ── Estado: error ──
                if status == "error":
                    err_code = info.get("errorCode", "?")
                    err_msg  = info.get("errorMessage", "Error desconocido")
                    log.error(f"Error aria2 [{task.gid}]: [{err_code}] {err_msg}")
                    await self._safe_edit(
                        task.status_msg,
                        f"❌ **Error de descarga**\n"
                        f"Código: `{err_code}`\n"
                        f"Detalle: `{err_msg[:100]}`"
                    )
                    break

                # ── Estado: removed ──
                if status == "removed":
                    await self._safe_edit(task.status_msg, "🗑 **Descarga eliminada**")
                    break

                # ── Estado: active / waiting / paused ──
                total     = int(info.get("totalLength", 0))
                completed = int(info.get("completedLength", 0))
                speed     = int(info.get("downloadSpeed", 0))

                progress_pct = (completed / total * 100) if total > 0 else 0

                # Detectar stall
                if completed > last_completed:
                    stall_time    = 0
                    last_completed = completed
                else:
                    stall_time += Config.UPDATE_INTERVAL
                    if stall_time >= MAX_STALL and total > 0:
                        log.warning(f"Stall detectado: {task.name}")
                        await self._safe_edit(
                            task.status_msg,
                            f"⚠️ **Sin progreso por {Utils.format_eta(MAX_STALL)}**\n"
                            f"Puede que no haya seeds disponibles.\n"
                            f"Usa `/cancel` para cancelar."
                        )
                        stall_time = 0  # reset para no spam

                # Actualizar mensaje solo si cambió suficiente o pasó tiempo
                now = time.time()
                progress_changed = abs(progress_pct - last_progress_pct) >= 1
                time_passed      = (now - last_update_time) >= Config.UPDATE_INTERVAL

                if progress_changed or time_passed:
                    last_update_time  = now
                    last_progress_pct = progress_pct

                    # Calcular ETA
                    eta = "∞"
                    if speed > 0 and total > completed:
                        eta = Utils.format_eta((total - completed) / speed)

                    # Obtener nombre real si es torrent
                    if status == "active" and task.source in ("magnet", "torrent"):
                        bt_info = info.get("bittorrent", {})
                        bt_name = bt_info.get("info", {}).get("name", "")
                        if bt_name and bt_name != task.name:
                            task.name = bt_name

                    bar = Utils.progress_bar(progress_pct)
                    status_emoji = {
                        "active":  "⬇️",
                        "waiting": "⏳",
                        "paused":  "⏸️"
                    }.get(status, "🔄")

                    text = (
                        f"{status_emoji} **{task.name[:40]}**\n\n"
                        f"`{bar}` `{progress_pct:.1f}%`\n"
                        f"📊 {Utils.format_speed(speed)}\n"
                        f"📥 {Utils.format_size(completed)} / {Utils.format_size(total)}\n"
                        f"⏱ ETA: {eta}"
                    )

                    await self._safe_edit(task.status_msg, text, parse_mode="markdown")

                await asyncio.sleep(Config.UPDATE_INTERVAL)

        except asyncio.CancelledError:
            log.info(f"Monitor cancelado: CID={cid}")

        except Exception as e:
            log.error(f"Error en monitor [{cid}]: {e}", exc_info=True)
            try:
                await self._safe_edit(task.status_msg, f"❌ **Error interno:** `{str(e)[:100]}`")
            except:
                pass

        finally:
            # Limpieza
            if cid in self.tasks:
                del self.tasks[cid]
            log.info(f"🏁 Monitor finalizado: CID={cid}")

    # ─── RECOLECCIÓN Y SUBIDA ─────────────────────────────────────────────────

    async def _collect_and_upload(self, cid: int, aria2_info: dict):
        """Recolecta los archivos descargados y los sube"""
        files_to_upload = []

        try:
            # Obtener archivos desde aria2
            aria_files = aria2_info.get("files", [])
            for f in aria_files:
                fp = f.get("path", "")
                if fp and os.path.isfile(fp) and os.path.getsize(fp) > 0:
                    # Excluir metadatos
                    if not fp.endswith((".torrent", ".aria2", ".session")):
                        files_to_upload.append(fp)

            # Si aria2 no dio archivos, buscar en el directorio
            if not files_to_upload:
                log.info("Buscando archivos en directorio...")
                for item in sorted(self.storage_path.rglob("*")):
                    if item.is_file() and item.stat().st_size > 0:
                        if not item.name.endswith((".torrent", ".aria2", ".session", ".dat", ".log")):
                            files_to_upload.append(str(item))

            # Ordenar por tamaño descendente
            files_to_upload.sort(key=lambda x: os.path.getsize(x), reverse=True)

        except Exception as e:
            log.error(f"Error recolectando archivos: {e}")

        if not files_to_upload:
            await self._safe_edit(
                self.tasks.get(cid, DownloadTask("", "", cid)).status_msg if cid in self.tasks else None,
                "⚠️ **No se encontraron archivos para subir**"
            )
            return

        log.info(f"📦 {len(files_to_upload)} archivo(s) para subir")
        await self._upload_files(cid, files_to_upload)

    async def _upload_files(self, cid: int, files: List[str]):
        """Sube lista de archivos al canal de Telegram"""
        task       = self.tasks.get(cid)
        status_msg = task.status_msg if task else None

        uploaded = 0
        failed   = 0
        skipped  = 0

        for fp in files:
            if not os.path.isfile(fp):
                continue

            size = os.path.getsize(fp)
            fn   = os.path.basename(fp)

            # Verificar tamaño máximo
            if size > Config.MAX_FILE_SIZE:
                log.warning(f"⚠️ {fn} demasiado grande: {Utils.format_size(size)}")
                await self.client.send_message(
                    cid,
                    f"⚠️ **Archivo demasiado grande para Telegram**\n"
                    f"`{fn}` — {Utils.format_size(size)}\n"
                    f"(Límite: {Utils.format_size(Config.MAX_FILE_SIZE)})",
                    parse_mode="markdown"
                )
                skipped += 1
                continue

            log.info(f"📤 Subiendo: {fn} ({Utils.format_size(size)})")

            try:
                if status_msg:
                    await self._safe_edit(
                        status_msg,
                        f"📤 **Subiendo al canal...**\n"
                        f"`{fn[:50]}`\n"
                        f"📦 {Utils.format_size(size)}",
                        parse_mode="markdown"
                    )

                # Verificar caché
                md5     = Utils.get_file_md5(fp)
                file_id = self.cache.get_file_id(md5) if md5 else None

                if file_id:
                    # Reenviar desde caché
                    log.info(f"♻️ Usando caché para {fn}")
                    msg = await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=file_id,
                        caption=f"📁 {fn}\n💾 {Utils.format_size(size)}",
                        force_document=True
                    )
                else:
                    # Subir archivo nuevo con progress callback
                    sent_size_ref = [0]

                    def progress_callback(sent, total):
                        sent_size_ref[0] = sent

                    msg = await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=fp,
                        caption=f"📁 {fn}\n💾 {Utils.format_size(size)}",
                        force_document=True,
                        progress_callback=progress_callback,
                    )

                    # Guardar en caché
                    if md5 and msg and hasattr(msg, "document") and msg.document:
                        self.cache.set_file_id(md5, str(msg.document.id), fn)

                log.info(f"✅ Subido: {fn}")
                uploaded += 1

                # Eliminar archivo local tras subir
                try:
                    os.remove(fp)
                    log.info(f"🗑 Eliminado local: {fn}")
                except Exception as e:
                    log.warning(f"No se pudo eliminar {fn}: {e}")

                await asyncio.sleep(1)

            except FloodWaitError as e:
                log.warning(f"FloodWait {e.seconds}s")
                await asyncio.sleep(e.seconds + 5)
                # Reintentar
                try:
                    await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=fp,
                        caption=f"📁 {fn}\n💾 {Utils.format_size(size)}",
                        force_document=True
                    )
                    uploaded += 1
                except Exception as e2:
                    log.error(f"Reintento fallido {fn}: {e2}")
                    failed += 1

            except ChatWriteForbiddenError:
                log.error("Bot sin permisos en el canal")
                await self.client.send_message(
                    cid,
                    "❌ **El bot no tiene permisos para enviar al canal**\n"
                    "Agrégalo como administrador.",
                    parse_mode="markdown"
                )
                break

            except Exception as e:
                log.error(f"❌ Error subiendo {fn}: {e}", exc_info=True)
                failed += 1

        # Mensaje resumen
        if uploaded == 0 and failed == 0 and skipped == 0:
            msg_final = "⚠️ **No se subió ningún archivo**"
        elif failed == 0 and skipped == 0:
            msg_final = f"✅ **¡Todo subido!**\n📦 {uploaded} archivo(s) enviado(s) al canal"
        elif failed > 0:
            msg_final = (
                f"⚠️ **Completado con errores**\n"
                f"✅ {uploaded} subido(s) | ❌ {failed} falló | ⏭ {skipped} omitido(s)"
            )
        else:
            msg_final = f"✅ **{uploaded} subido(s)** | ⏭ {skipped} omitido(s) (muy grandes)"

        await self.client.send_message(cid, msg_final, parse_mode="markdown")

        if status_msg:
            await self._safe_delete(status_msg)

    # ─── AYUDANTES ───────────────────────────────────────────────────────────

    def _check_no_active(self, cid: int) -> bool:
        """True si NO hay tarea activa para este chat"""
        return cid not in self.tasks

    async def _cancel_task(self, cid: int, notify: bool = False):
        """Cancela tarea activa para un chat"""
        if cid not in self.tasks:
            return

        task = self.tasks[cid]

        # Cancelar monitor
        if task.monitor_task and not task.monitor_task.done():
            task.monitor_task.cancel()
            try:
                await asyncio.wait_for(
                    asyncio.shield(task.monitor_task), timeout=3
                )
            except:
                pass

        # Cancelar en aria2
        self.aria2.remove(task.gid)
        self.aria2.purge(task.gid)

        # Eliminar mensaje de estado
        if task.status_msg:
            await self._safe_delete(task.status_msg)

        del self.tasks[cid]
        log.info(f"✓ Tarea cancelada: CID={cid} GID={task.gid}")

    def _format_status(self, task: DownloadTask, info: dict) -> str:
        """Formatea mensaje de estado"""
        total     = int(info.get("totalLength", 0))
        completed = int(info.get("completedLength", 0))
        speed     = int(info.get("downloadSpeed", 0))
        status    = info.get("status", "?")

        progress = (completed / total * 100) if total > 0 else 0
        bar      = Utils.progress_bar(progress)

        eta = "∞"
        if speed > 0 and total > completed:
            eta = Utils.format_eta((total - completed) / speed)

        return (
            f"📊 **Estado de descarga**\n\n"
            f"📁 `{task.name[:40]}`\n"
            f"🔄 Estado: `{status}`\n\n"
            f"`{bar}` `{progress:.1f}%`\n"
            f"📊 {Utils.format_speed(speed)}\n"
            f"📥 {Utils.format_size(completed)} / {Utils.format_size(total)}\n"
            f"⏱ ETA: {eta}\n"
            f"⏳ Tiempo: {Utils.format_eta(task.elapsed)}"
        )

    @staticmethod
    async def _safe_edit(msg, text: str, parse_mode: str = "markdown"):
        """Edita mensaje ignorando errores comunes"""
        if msg is None:
            return
        try:
            await msg.edit(text, parse_mode=parse_mode)
        except MessageNotModifiedError:
            pass
        except MessageIdInvalidError:
            log.warning("Mensaje inválido al intentar editar")
        except FloodWaitError as e:
            await asyncio.sleep(e.seconds + 1)
        except Exception as e:
            log.warning(f"_safe_edit: {e}")

    @staticmethod
    async def _safe_delete(msg):
        """Elimina mensaje ignorando errores"""
        if msg is None:
            return
        try:
            await msg.delete()
        except Exception:
            pass

    # ─── CIERRE ──────────────────────────────────────────────────────────────

    async def stop(self):
        """Cierra el bot limpiamente"""
        log.info("🛑 Cerrando bot...")

        # Cancelar todas las tareas activas
        for cid in list(self.tasks.keys()):
            await self._cancel_task(cid)

        # Desconectar Telegram
        try:
            await self.client.disconnect()
        except:
            pass

        # Detener aria2
        Aria2Manager.stop()

        # Limpiar archivos temporales
        self._cleanup_temp()

        log.info("✓ Bot cerrado correctamente")

    def _cleanup_temp(self):
        """Limpia archivos temporales"""
        try:
            for f in self.storage_path.glob("tmp_*"):
                try:
                    f.unlink()
                except:
                    pass
        except:
            pass

# ═══════════════════════════════════════════════════════════════════════════════
# PUNTO DE ENTRADA
# ═══════════════════════════════════════════════════════════════════════════════
async def main():
    bot = TeleTorrentBot()

    # Manejar señales de sistema
    loop = asyncio.get_event_loop()

    def handle_signal(sig):
        log.info(f"Señal recibida: {sig.name}")
        asyncio.create_task(bot.stop())

    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(sig, lambda s=sig: handle_signal(s))
        except NotImplementedError:
            pass  # Windows no soporta add_signal_handler

    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción por teclado")
    except Exception as e:
        log.error(f"Error fatal: {e}", exc_info=True)
    finally:
        await bot.stop()


if __name__ == "__main__":
    asyncio.run(main())
