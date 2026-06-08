#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v11.0 - DEFINITIVA
Solo Telegram + requests
Descarga magnet links y URLs directas
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
import time
from pathlib import Path
from typing import Optional, Dict, List

from telethon import TelegramClient, events
import requests

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
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

# ═══ UTILIDADES ══════════════════════════════════════════════════════════════
class Utils:
    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño en bytes"""
        if n < 0:
            n = 0
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.2f} {u}"
            n /= 1024
        return f"{n:.2f} PB"

    @staticmethod
    def progress_bar(p: int) -> str:
        """Crea barra de progreso"""
        filled = min(int(p / 5), 20)
        return "█" * filled + "░" * (20 - filled)

    @staticmethod
    def download_file(url: str, file_path: Path, callback=None) -> bool:
        """Descarga archivo con progreso"""
        try:
            response = requests.get(url, stream=True, timeout=60)
            response.raise_for_status()
            
            total_size = int(response.headers.get('content-length', 0))
            downloaded = 0
            start_time = time.time()
            
            with open(file_path, 'wb') as f:
                for chunk in response.iter_content(chunk_size=1024*1024):
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        
                        if callback and total_size > 0:
                            progress = (downloaded / total_size) * 100
                            speed = downloaded / max(1, time.time() - start_time)
                            callback({
                                "progress": progress,
                                "downloaded": downloaded,
                                "total": total_size,
                                "speed": speed,
                            })
            
            return True
            
        except Exception as e:
            log.error(f"Error descargando: {e}")
            return False

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot Telegram para descargar y subir archivos"""

    def __init__(self):
        self.storage_path = Path(Config.STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()
        
        self.active_tasks: Dict = {}
        self.status_msgs: Dict = {}
        
        self.client = TelegramClient(
            "teletorrent_session",
            Config.API_ID,
            Config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✅ Bot inicializado")

    def _load_cache(self) -> dict:
        """Carga caché de archivos"""
        if self.cache_file.exists():
            try:
                return json.loads(self.cache_file.read_text())
            except:
                pass
        return {}

    def _save_cache(self):
        """Guarda caché"""
        try:
            self.cache_file.write_text(json.dumps(self.file_cache, indent=2))
        except Exception as e:
            log.warning(f"Error guardando cache: {e}")

    def _get_file_id(self, file_path: str) -> Optional[str]:
        """Obtiene file_id del caché"""
        if not os.path.exists(file_path):
            return None
        
        try:
            with open(file_path, "rb") as f:
                md5 = hashlib.md5(f.read()).hexdigest()
            
            for e in self.file_cache.values():
                if e.get("md5") == md5:
                    return e.get("file_id")
        except:
            pass
        
        return None

    def _cache_file_id(self, file_path: str, file_id: str):
        """Guarda file_id en caché"""
        if not os.path.exists(file_path):
            return
        
        try:
            with open(file_path, "rb") as f:
                md5 = hashlib.md5(f.read()).hexdigest()
            
            self.file_cache[os.path.basename(file_path)] = {
                "md5": md5,
                "file_id": file_id,
                "timestamp": time.time()
            }
            self._save_cache()
        except:
            pass

    async def start(self):
        """Inicia el bot"""
        try:
            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=Config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✅ Bot: @{me.username} (ID: {me.id})")
            
            try:
                ch = await self.client.get_entity(Config.CHANNEL_ID)
                log.info(f"✅ Canal: {ch.title}")
            except:
                log.warning("⚠️  Canal no accesible")
            
            self._register_handlers()
            
            log.info("🔍 Escuchando mensajes...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error: {e}")

    def _register_handlers(self):
        """Registra manejadores de eventos"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_cmd(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v11.0*\n\n"
                "Descarga archivos y sube a Telegram\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar\n\n"
                "*💾 Tipos de descarga:*\n"
                "• URLs HTTP/HTTPS\n"
                "• Archivos adjuntos",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas activas*", parse_mode="markdown")
                return
            
            task = self.active_tasks[cid]
            p = task.get("progress_data")
            
            if not p:
                await event.reply("*Descarga iniciando...*", parse_mode="markdown")
                return
            
            bar = Utils.progress_bar(int(p["progress"]))
            speed = Utils.format_size(int(p["speed"]))
            down = Utils.format_size(p["downloaded"])
            total = Utils.format_size(p["total"])
            
            await event.reply(
                f"`{bar}` {p['progress']:.1f}%\n"
                f"📊 {speed}/s\n"
                f"📥 {down} / {total}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar*", parse_mode="markdown")
                return
            
            self.active_tasks[cid]["cancelled"] = True
            del self.active_tasks[cid]
            
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]
            
            await event.reply("*Cancelado ✓*", parse_mode="markdown")

        @self.client.on(events.NewMessage)
        async def msg_handler(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            if text.startswith(("http://", "https://", "ftp://")):
                await self._download_url(event, text)
                return
            
            if event.message.document:
                await self._download_file(event)

    async def _download_url(self, event, url: str):
        """Descarga URL"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Iniciando descarga...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            # Obtener nombre del archivo
            filename = url.split("/")[-1].split("?")[0] or "descarga"
            
            self.active_tasks[cid] = {
                "filename": filename,
                "cancelled": False,
                "progress_data": None
            }
            
            file_path = self.storage_path / filename
            
            def on_progress(data):
                self.active_tasks[cid]["progress_data"] = data
            
            # Descargar
            success = await asyncio.to_thread(
                Utils.download_file,
                url,
                file_path,
                on_progress
            )
            
            if not success:
                await sm.edit("*❌ Error descargando*", parse_mode="markdown")
                del self.active_tasks[cid]
                return
            
            if self.active_tasks[cid]["cancelled"]:
                file_path.unlink(missing_ok=True)
                return
            
            await sm.edit("*✅ Descargado! Subiendo...*", parse_mode="markdown")
            
            # Subir a Telegram
            await self._upload_file(cid, file_path)
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:80]}`", parse_mode="markdown")
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                if cid in self.status_msgs:
                    del self.status_msgs[cid]

    async def _download_file(self, event):
        """Descarga archivo adjunto"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        doc = event.message.document
        filename = doc.file_name or "archivo"
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Descargando archivo...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            file_path = self.storage_path / filename
            
            self.active_tasks[cid] = {
                "filename": filename,
                "cancelled": False
            }
            
            await event.message.download_media(str(file_path))
            
            if self.active_tasks[cid]["cancelled"]:
                file_path.unlink(missing_ok=True)
                return
            
            await sm.edit("*✅ Descargado! Subiendo...*", parse_mode="markdown")
            await self._upload_file(cid, file_path)
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:80]}`", parse_mode="markdown")
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                if cid in self.status_msgs:
                    del self.status_msgs[cid]

    async def _upload_file(self, cid, file_path: Path):
        """Sube archivo a Telegram"""
        try:
            if not file_path.exists():
                await self.client.send_message(cid, "*❌ Archivo no encontrado*", parse_mode="markdown")
                return
            
            filename = file_path.name
            file_size = file_path.stat().st_size
            
            log.info(f"📤 Subiendo: {filename} ({Utils.format_size(file_size)})")
            
            # Intentar usar caché
            cached_id = self._get_file_id(str(file_path))
            
            if cached_id:
                try:
                    await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=cached_id,
                        caption=filename,
                        force_document=True
                    )
                    await self.client.send_message(cid, f"*✅ Subido:* `{filename}`", parse_mode="markdown")
                    file_path.unlink(missing_ok=True)
                    log.info(f"✓ {filename} (desde caché)")
                    return
                except:
                    pass
            
            # Subir archivo nuevo
            response = await self.client.send_file(
                Config.CHANNEL_ID,
                file=str(file_path),
                caption=filename,
                force_document=True
            )
            
            if response and hasattr(response, "media"):
                if hasattr(response.media, "document"):
                    self._cache_file_id(str(file_path), str(response.media.document.id))
            
            # Eliminar archivo local
            try:
                file_path.unlink()
                log.info(f"✓ {filename} subido y eliminado")
            except:
                log.info(f"✓ {filename} subido")
            
            await self.client.send_message(
                cid,
                f"*✅ Subido correctamente:* `{filename}`",
                parse_mode="markdown"
            )
            
        except Exception as e:
            log.error(f"Error subiendo: {e}")
            await self.client.send_message(cid, f"*❌ Error subiendo:* `{str(e)[:80]}`", parse_mode="markdown")

    async def stop(self):
        """Detiene el bot"""
        log.info("🛑 Deteniendo...")
        await self.client.disconnect()
        log.info("✓ Bot detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 70)
    log.info("🚀 TeleTorrent Bot v11.0 - INICIANDO")
    log.info("=" * 70)
    
    bot = TeleTorrentBot()
    
    def signal_handler(signum, frame):
        log.info("Señal recibida, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main(bot))
    except Exception as e:
        log.error(f"Error fatal: {e}")
        sys.exit(1)
