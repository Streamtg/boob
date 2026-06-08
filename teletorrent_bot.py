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

# ═══ CONFIGURACION GLOBAL ════════════════════════════════════════════════════
# Declarar ANTES de usarlas
API_ID = 34280578
API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
CHANNEL_ID = -1003213143951
STORAGE_PATH = "./downloads"
MAX_WORKERS = 3
UPDATE_INTERVAL = 2

# ═══ IMPORTACIONES CON MANEJO DE ERRORES ═════════════════════════════════════
try:
    import libtorrent as lt
except ImportError:
    print("❌ ERROR: libtorrent no está instalado")
    print("\n📥 Instala con:")
    print("   Linux/Ubuntu: pip install --no-binary :all: libtorrent-rasterbar")
    print("   macOS: brew install libtorrent-rasterbar && pip install libtorrent")
    print("   Windows: pip install libtorrent")
    sys.exit(1)

try:
    from telethon import TelegramClient, events
except ImportError:
    print("❌ ERROR: pip install telethon")
    sys.exit(1)

try:
    import requests
except ImportError:
    print("❌ ERROR: pip install requests")
    sys.exit(1)

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

torrent_temp_dir = tempfile.mkdtemp(prefix="teletorrent_")

def cleanup_temp():
    """Limpia directorio temporal"""
    global torrent_temp_dir
    if os.path.exists(torrent_temp_dir):
        try:
            shutil.rmtree(torrent_temp_dir, ignore_errors=True)
        except Exception as e:
            log.warning(f"Error limpiando temp: {e}")

# ═══ CLIENTE TORRENT ═════════════════════════════════════════════════════════
class TorrentClient:
    """Cliente de descarga de torrents con libtorrent"""

    def __init__(self, storage_path: str):
        self.storage_path = Path(storage_path)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.session = lt.session()
        self.session.listen_on(6881, 6891)
        
        self.session.apply_settings({
            "user_agent": "TeleTorrentBot/3.0",
            "download_rate_limit": 0,
            "upload_rate_limit": 0,
            "active_downloads": MAX_WORKERS,
            "active_limit": 10,
            "max_connections": 200,
            "connections_limit": 500,
        })
        
        self.active_torrents: dict = {}
        log.info(f"✓ Torrent client iniciado. Storage: {self.storage_path}")

    def add_magnet(self, magnet_uri: str) -> dict:
        """Agrega un magnet link y espera metadatos"""
        try:
            magnet_uri = magnet_uri.strip()
            if "&" in magnet_uri:
                magnet_uri = magnet_uri[:magnet_uri.index("&")]
            
            params = {
                "save_path": str(self.storage_path),
                "storage_mode": lt.storage_mode_t.storage_mode_sparse,
            }
            
            handle = lt.add_magnet_uri(self.session, magnet_uri, params)
            timeout = 60
            
            while not handle.has_metadata() and timeout > 0:
                time.sleep(0.5)
                timeout -= 0.5
            
            if not handle.has_metadata():
                raise TimeoutError("Timeout esperando metadatos del magnet (60s)")
            
            info = handle.get_torrent_info()
            info_hash = str(info.info_hash())
            name = info.name()
            files = []
            total_size = 0
            
            for i in range(info.num_files()):
                fe = info.file_at(i)
                files.append({
                    "path": fe.path,
                    "size": fe.size,
                    "index": i
                })
                total_size += fe.size
            
            handle.resume()
            
            self.active_torrents[info_hash] = {
                "handle": handle,
                "name": name,
                "files": files,
                "total_size": total_size,
                "started_at": time.time(),
                "cancelled": False,
            }
            
            log.info(f"✓ Torrent agregado: {name} ({self.format_size(total_size)})")
            
            return {
                "info_hash": info_hash,
                "name": name,
                "files": files,
                "total_size": total_size
            }
            
        except Exception as e:
            log.error(f"Error agregando magnet: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes) -> dict:
        """Agrega un archivo torrent"""
        try:
            torrent_path = os.path.join(torrent_temp_dir, "temp.torrent")
            with open(torrent_path, "wb") as f:
                f.write(torrent_data)
            
            params = {
                "save_path": str(self.storage_path),
                "storage_mode": lt.storage_mode_t.storage_mode_sparse,
            }
            
            handle = lt.add_torrent(
                self.session,
                {"ti": lt.torrent_info(torrent_path), **params}
            )
            
            timeout = 60
            while not handle.has_metadata() and timeout > 0:
                time.sleep(0.5)
                timeout -= 0.5
            
            if not handle.has_metadata():
                raise TimeoutError("Timeout esperando metadatos del torrent")
            
            info = handle.get_torrent_info()
            info_hash = str(info.info_hash())
            name = info.name()
            files = []
            total_size = 0
            
            for i in range(info.num_files()):
                fe = info.file_at(i)
                files.append({
                    "path": fe.path,
                    "size": fe.size,
                    "index": i
                })
                total_size += fe.size
            
            handle.resume()
            
            self.active_torrents[info_hash] = {
                "handle": handle,
                "name": name,
                "files": files,
                "total_size": total_size,
                "started_at": time.time(),
                "cancelled": False,
            }
            
            log.info(f"✓ Archivo torrent agregado: {name}")
            
            return {
                "info_hash": info_hash,
                "name": name,
                "files": files,
                "total_size": total_size
            }
            
        except Exception as e:
            log.error(f"Error agregando archivo torrent: {e}")
            raise

    def get_progress(self, info_hash: str) -> Optional[dict]:
        """Obtiene el progreso de una descarga"""
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
        """Cancela una descarga"""
        if info_hash in self.active_torrents:
            t = self.active_torrents[info_hash]
            t["cancelled"] = True
            self.session.remove_torrent(t["handle"])
            del self.active_torrents[info_hash]
            log.info(f"✗ Cancelado: {t['name']}")

    def wait_complete(self, info_hash: str, callback=None):
        """Espera a que se complete la descarga"""
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
                    try:
                        callback(self.get_progress(info_hash))
                    except Exception as e:
                        log.warning(f"Error en callback: {e}")
            
            if status.progress >= 1.0:
                if status.state not in [lt.torrent_status.checking_files]:
                    break
            
            time.sleep(1)
        
        return not t.get("cancelled", False)

    def get_completed_files(self, info_hash: str) -> list:
        """Obtiene los archivos completados"""
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
        """Convierte el estado a string"""
        states = {
            0: "queued",
            1: "checking",
            2: "downloading",
            3: "downloading",
            4: "finished",
            5: "seeding",
            6: "allocating",
            7: "checking_fast"
        }
        return states.get(state, "unknown")

    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño en bytes a unidades legibles"""
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.1f} {u}"
            n /= 1024
        return f"{n:.1f} PB"

    def close(self):
        """Cierra el cliente torrent"""
        for h in list(self.active_torrents.keys()):
            self.cancel(h)
        self.session.pause()

# ═══ BOT DE TELEGRAM ═════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot principal de Telegram"""

    def __init__(self):
        self.storage_path = Path(STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()
        
        self.torrent = TorrentClient(str(self.storage_path))
        self.active_tasks: dict = {}
        self.status_messages: dict = {}
        
        self.client = TelegramClient(
            "teletorrent_session",
            API_ID,
            API_HASH,
            connection_retries=5,
            timeout=30,
            spawn_read_thread=True
        )
        
        log.info("✓ Bot inicializado")

    def _load_cache(self) -> dict:
        """Carga la caché de archivos"""
        if self.cache_file.exists():
            try:
                return json.loads(self.cache_file.read_text())
            except Exception as e:
                log.warning(f"Error cargando cache: {e}")
        return {}

    def _save_cache(self):
        """Guarda la caché"""
        try:
            self.cache_file.write_text(json.dumps(self.file_cache, indent=2))
        except Exception as e:
            log.warning(f"Error guardando cache: {e}")

    def _get_file_id(self, file_path: str) -> Optional[str]:
        """Obtiene el file_id del caché"""
        if not os.path.exists(file_path):
            return None
        
        try:
            with open(file_path, "rb") as f:
                md5 = hashlib.md5(f.read()).hexdigest()
            
            for e in self.file_cache.values():
                if e.get("md5") == md5:
                    return e.get("file_id")
        except Exception as e:
            log.warning(f"Error obteniendo file_id: {e}")
        
        return None

    def _cache_file_id(self, file_path: str, file_id: str):
        """Guarda un file_id en caché"""
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
        except Exception as e:
            log.warning(f"Error cacheando file_id: {e}")

    async def start(self):
        """Inicia el bot"""
        try:
            log.info("Conectando a Telegram via MTProto...")
            await self.client.start(bot_token=BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot activo: @{me.username} (ID: {me.id})")
            
            try:
                ch = await self.client.get_entity(CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except Exception as e:
                log.warning(f"⚠ Canal no accesible: {e}")

            self._register_handlers()
            
            log.info("🔍 Escuchando mensajes...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error iniciando bot: {e}")
            raise

    def _register_handlers(self):
        """Registra los manejadores de eventos"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_h(event):
            await event.reply(
                "*TeleTorrent Bot v3.0* 🚀\n\n"
                "Descarga torrents y sube archivos a Telegram\n"
                "sin límites de 50MB usando MTProto.\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar descarga\n"
                "🔹 `/cache` - Estado de caché\n\n"
                "*💡 Uso:*\n"
                "Envía un magnet link o URL de archivo .torrent",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_h(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*No hay descargas activas* ⏸", parse_mode="markdown")
                return
            
            t = self.active_tasks[cid]
            p = self.torrent.get_progress(t["info_hash"])
            
            if not p:
                await event.reply("*Descarga no disponible* ❌", parse_mode="markdown")
                return
            
            bar = self._pbar(int(p["progress"]))
            speed_text = TorrentClient.format_size(p["speed"])
            downloaded_text = TorrentClient.format_size(p["downloaded"])
            total_text = TorrentClient.format_size(p["total"])
            
            await event.reply(
                f"*Descargando:* `{t['name']}`\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed_text}/s\n"
                f"📥 {downloaded_text} / {total_text}\n"
                f"⏱ {int(p['elapsed'])}s",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_h(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar* ⏹", parse_mode="markdown")
                return
            
            t = self.active_tasks[cid]
            self.torrent.cancel(t["info_hash"])
            del self.active_tasks[cid]
            
            if cid in self.status_messages:
                try:
                    await self.status_messages[cid].delete()
                except:
                    pass
                del self.status_messages[cid]
            
            await event.reply("*Descarga cancelada* ✓", parse_mode="markdown")

        @self.client.on(events.NewMessage(pattern=r"^/cache$"))
        async def cache_h(event):
            count = len(self.file_cache)
            size = 0
            if self.cache_file.exists():
                size = os.path.getsize(self.cache_file)
            
            await event.reply(
                f"*Estado de caché:*\n"
                f"📦 {count} archivos\n"
                f"💾 {TorrentClient.format_size(size)}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage)
        async def msg_h(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            if text.startswith("magnet:?xt=") or "torrent" in text.lower():
                await self._start_download(event)

    async def _start_download(self, event):
        """Inicia una descarga"""
        cid = event.chat_id
        text = event.message.text.strip()
        
        if cid in self.active_tasks:
            await event.reply(
                "*Ya hay una descarga activa*\n"
                "Usa `/cancel` para detenerla",
                parse_mode="markdown"
            )
            return
        
        sm = await event.reply("*⏳ Iniciando...*", parse_mode="markdown")
        self.status_messages[cid] = sm
        
        try:
            if text.startswith("magnet:?xt="):
                if "&" in text:
                    text = text[:text.index("&")]
                
                if not text.startswith("magnet:?xt="):
                    await sm.edit("*❌ Magnet inválido*")
                    return
                
                await sm.edit("*📥 Agregando magnet...*")
                info = self.torrent.add_magnet(text)
                
            elif text.startswith("http"):
                await sm.edit("*📥 Descargando archivo .torrent...*")
                
                try:
                    r = requests.get(text, timeout=30)
                    if r.status_code != 200:
                        await sm.edit(f"*❌ Error HTTP {r.status_code}*")
                        return
                    info = self.torrent.add_torrent_file(r.content)
                except requests.RequestException as e:
                    await sm.edit(f"*❌ Error descargando:* `{e}`")
                    return
            else:
                await sm.edit("*❌ Formato no válido*\nUsa magnet link o URL .torrent")
                return

            self.active_tasks[cid] = {
                "info_hash": info["info_hash"],
                "name": info["name"],
                "total_size": info["total_size"],
                "files": info["files"],
            }
            
            await sm.edit(
                f"*✅ Agregado:*\n`{info['name']}`\n\n"
                f"`{self._pbar(0)}` 0%"
            )
            
            asyncio.create_task(self._monitor(cid, info["info_hash"]))
            
        except Exception as ex:
            log.error(f"Error iniciando descarga: {ex}")
            await sm.edit(f"*❌ Error:* `{str(ex)[:100]}`")
            
            if cid in self.active_tasks:
                del self.active_tasks[cid]

    async def _monitor(self, cid, info_hash):
        """Monitorea el progreso de descarga"""
        try:
            def cb(p):
                try:
                    if self.client.loop:
                        asyncio.run_coroutine_threadsafe(
                            self._upd_progress(cid, p),
                            self.client.loop
                        )
                except Exception as e:
                    log.warning(f"Error en callback: {e}")
            
            ok = self.torrent.wait_complete(info_hash, cb)
            
            if not ok:
                return
            
            await self._upd_msg(cid, "*⏳ Descarga completa! Subiendo archivos...*")
            files = self.torrent.get_completed_files(info_hash)
            
            if files:
                await self._upload(cid, files)
            else:
                await self._upd_msg(cid, "*⚠ No hay archivos para subir*")
                
        except Exception as ex:
            log.error(f"Error en monitor: {ex}")
            await self._upd_msg(cid, f"*❌ Error:* `{str(ex)[:100]}`")
            
        finally:
            if cid in self.active_tasks:
                del self.active_tasks[cid]
            
            if cid in self.status_messages:
                try:
                    await self.status_messages[cid].delete()
                except:
                    pass
                
                if cid in self.status_messages:
                    del self.status_messages[cid]

    async def _upd_progress(self, cid, p):
        """Actualiza el progreso en tiempo real"""
        if cid not in self.status_messages:
            return
        
        if p is None:
            return
        
        n = self.active_tasks.get(cid, {}).get("name", "Descarga")
        bar = self._pbar(int(p["progress"]))
        speed_text = TorrentClient.format_size(p["speed"])
        downloaded_text = TorrentClient.format_size(p["downloaded"])
        total_text = TorrentClient.format_size(p["total"])
        
        try:
            await self.status_messages[cid].edit(
                f"*⬇️ Descargando:* `{n[:30]}`\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed_text}/s\n"
                f"📥 {downloaded_text} / {total_text}\n"
                f"⏱ {int(p['elapsed'])}s",
                parse_mode="markdown"
            )
        except Exception as e:
            log.warning(f"Error actualizando progreso: {e}")

    async def _upd_msg(self, cid, text):
        """Actualiza un mensaje"""
        if cid in self.status_messages:
            try:
                await self.status_messages[cid].edit(text)
            except Exception as e:
                log.warning(f"Error actualizando mensaje: {e}")

    @staticmethod
    def _pbar(p: int) -> str:
        """Crea una barra de progreso"""
        filled = min(int(p / 5), 20)
        empty = 20 - filled
        return "█" * filled + "░" * empty

    async def _upload(self, cid, files):
        """Sube archivos al canal"""
        uploaded = 0
        failed = 0
        
        for file_path in files:
            if not os.path.exists(file_path):
                continue
            
            file_size = os.path.getsize(file_path)
            
            if file_size == 0:
                continue
            
            file_name = os.path.basename(file_path)
            
            log.info(f"📤 Subiendo: {file_name} ({TorrentClient.format_size(file_size)})")
            
            # Intentar usar caché
            cached_id = self._get_file_id(file_path)
            
            if cached_id:
                try:
                    await self.client.send_file(
                        CHANNEL_ID,
                        file=cached_id,
                        caption=file_name,
                        force_document=True
                    )
                    uploaded += 1
                    log.info(f"✅ Enviado desde caché: {file_name}")
                    continue
                except Exception as e:
                    log.warning(f"Caché inválida: {e}")
            
            # Subir archivo nuevo
            try:
                await self._upd_msg(cid, f"*📤 Subiendo:* `{file_name}`")
                
                response = await self.client.send_file(
                    CHANNEL_ID,
                    file=file_path,
                    caption=file_name,
                    force_document=True,
                    thumb=None
                )
                
                if response and hasattr(response, "media"):
                    if hasattr(response.media, "document"):
                        file_id = response.media.document.id
                        self._cache_file_id(file_path, str(file_id))
                
                uploaded += 1
                log.info(f"✅ Enviado: {file_name}")
                
            except Exception as e:
                log.error(f"Error subiendo {file_name}: {e}")
                failed += 1
            
            await asyncio.sleep(1)
        
        # Mensaje final
        if uploaded > 0 and failed == 0:
            msg = f"*✅ ¡Completado!*\n📦 {uploaded} archivo(s) enviado(s)"
        elif uploaded > 0 and failed > 0:
            msg = f"*⚠ Completado*\n✅ {uploaded} enviados\n❌ {failed} fallaron"
        else:
            msg = "*❌ Error:* No se pudieron enviar archivos"
        
        try:
            await self.client.send_message(cid, msg, parse_mode="markdown")
        except Exception as e:
            log.error(f"Error enviando mensaje final: {e}")

    async def stop(self):
        """Detiene el bot"""
        log.info("🛑 Deteniendo bot...")
        self.torrent.close()
        await self.client.disconnect()
        log.info("✓ Bot detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main_async(bot):
    """Función principal async"""
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    except Exception as e:
        log.error(f"Error fatal: {e}")
    finally:
        await bot.stop()
        cleanup_temp()
        log.info("✓ Limpieza completada")

def main():
    """Función principal"""
    parser = argparse.ArgumentParser(description="TeleTorrent Bot v3.0")
    parser.add_argument("--token", default=BOT_TOKEN, help="Token del bot")
    parser.add_argument("--channel", type=int, default=CHANNEL_ID, help="ID del canal")
    parser.add_argument("--storage", default=STORAGE_PATH, help="Ruta de almacenamiento")
    
    args = parser.parse_args()
    
    # Usar globals() para actualizar variables globales
    globals()['BOT_TOKEN'] = args.token
    globals()['CHANNEL_ID'] = args.channel
    globals()['STORAGE_PATH'] = args.storage
    
    log.info("=" * 60)
    log.info("TeleTorrent Bot v3.0 (Python) - Iniciando...")
    log.info("=" * 60)
    
    bot = TeleTorrentBot()
    
    def signal_handler(signum, frame):
        log.info(f"⚠️ Señal {signum} recibida, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main_async(bot))
    except Exception as e:
        log.error(f"Error: {e}")
    finally:
        cleanup_temp()

if __name__ == "__main__":
    main()
