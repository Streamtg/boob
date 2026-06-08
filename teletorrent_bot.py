#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v8.0 - PROFESIONAL CON TRANSMISSION
Usa Transmission RPC - Gestor de torrents real y profesional
Descarga torrents correctamente, sube a Telegram, limpia automáticamente
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
import time
import threading
from pathlib import Path
from typing import Optional, Dict, List

# ═══ IMPORTACIONES ═══════════════════════════════════════════════════════════
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

try:
    import transmissionrpc
except ImportError:
    print("❌ ERROR: pip install transmission-rpc")
    sys.exit(1)

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
    """Configuración centralizada"""
    
    # Telegram
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    
    # Transmission
    TRANSMISSION_HOST = "127.0.0.1"
    TRANSMISSION_PORT = 9091
    TRANSMISSION_USERNAME = ""
    TRANSMISSION_PASSWORD = ""
    
    # Rutas
    STORAGE_PATH = "./downloads"
    DOWNLOAD_DIR = "/home/codespace/downloads/torrents"

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ CLIENTE TRANSMISSION ════════════════════════════════════════════════════
class TransmissionClient:
    """Cliente Transmission RPC para torrents reales"""

    def __init__(self):
        """Inicializa cliente Transmission"""
        self.torrents: Dict = {}
        self.lock = threading.Lock()
        
        # Conectar a Transmission
        try:
            self.client = transmissionrpc.Client(
                host=Config.TRANSMISSION_HOST,
                port=Config.TRANSMISSION_PORT,
                username=Config.TRANSMISSION_USERNAME or None,
                password=Config.TRANSMISSION_PASSWORD or None,
            )
            
            # Verificar conexión
            session = self.client.get_session()
            log.info(f"✅ Transmission conectado")
            log.info(f"📁 Directorio: {session.download_dir}")
            
            # Asegurar que el directorio existe
            Path(session.download_dir).mkdir(parents=True, exist_ok=True)
            
        except Exception as e:
            log.error(f"❌ No se pudo conectar a Transmission")
            log.error(f"Asegúrate de que Transmission está corriendo:")
            log.error(f"  transmission-daemon")
            log.error(f"Error: {e}")
            sys.exit(1)

    def add_magnet(self, magnet_uri: str) -> Dict:
        """Agrega un magnet link a Transmission"""
        try:
            log.info(f"📥 Agregando magnet a Transmission...")
            
            # Agregar al cliente
            torrent = self.client.add_torrent(magnet_uri)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                    "magnet": magnet_uri,
                    "started_at": time.time(),
                }
            
            log.info(f"✓ Magnet agregado: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
                "hash_string": torrent.hashString,
            }
            
        except Exception as e:
            log.error(f"Error agregando magnet: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes, filename: str) -> Dict:
        """Agrega un archivo .torrent a Transmission"""
        try:
            log.info(f"📥 Agregando torrent a Transmission...")
            
            # Guardar archivo temporalmente
            temp_path = Path("/tmp") / f"temp_{int(time.time())}.torrent"
            temp_path.write_bytes(torrent_data)
            
            # Agregar al cliente
            torrent = self.client.add_torrent(str(temp_path))
            
            # Eliminar archivo temporal
            temp_path.unlink(missing_ok=True)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                    "filename": filename,
                    "started_at": time.time(),
                }
            
            log.info(f"✓ Torrent agregado: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
                "hash_string": torrent.hashString,
            }
            
        except Exception as e:
            log.error(f"Error agregando torrent: {e}")
            raise

    def get_torrent(self, torrent_id: int):
        """Obtiene información del torrent"""
        try:
            return self.client.get_torrent(torrent_id)
        except:
            return None

    def get_progress(self, torrent_id: int) -> Optional[Dict]:
        """Obtiene el progreso del torrent"""
        try:
            torrent = self.get_torrent(torrent_id)
            if not torrent:
                return None
            
            return {
                "name": torrent.name,
                "progress": torrent.progress,
                "downloaded": torrent.downloaded,
                "total": torrent.total_size,
                "speed": torrent.rateDownload,
                "status": torrent.status,
                "eta": torrent.eta,
                "peers": torrent.num_seeds,
            }
            
        except Exception as e:
            log.warning(f"Error obteniendo progreso: {e}")
            return None

    def get_files(self, torrent_id: int) -> List[str]:
        """Obtiene archivos descargados"""
        try:
            torrent = self.get_torrent(torrent_id)
            if not torrent:
                return []
            
            files = []
            session = self.client.get_session()
            download_dir = Path(session.download_dir)
            
            # Obtener archivos del torrent
            for file in torrent.files():
                file_path = download_dir / file['name']
                if file_path.exists() and file_path.stat().st_size > 0:
                    files.append(str(file_path))
            
            return files
            
        except Exception as e:
            log.warning(f"Error obteniendo archivos: {e}")
            return []

    def remove_torrent(self, torrent_id: int, delete_data: bool = True):
        """Elimina un torrent"""
        try:
            self.client.remove_torrent(torrent_id, delete_data=delete_data)
            
            with self.lock:
                if torrent_id in self.torrents:
                    del self.torrents[torrent_id]
            
            log.info(f"✓ Torrent eliminado: {torrent_id}")
            
        except Exception as e:
            log.warning(f"Error eliminando torrent: {e}")

    def start_torrent(self, torrent_id: int):
        """Inicia la descarga del torrent"""
        try:
            self.client.start_torrent(torrent_id)
            log.info(f"🚀 Torrent iniciado: {torrent_id}")
        except Exception as e:
            log.warning(f"Error iniciando torrent: {e}")

    def wait_completion(self, torrent_id: int, timeout: int = 3600) -> bool:
        """Espera a que se complete el torrent"""
        try:
            start_time = time.time()
            
            while time.time() - start_time < timeout:
                torrent = self.get_torrent(torrent_id)
                if not torrent:
                    return False
                
                if torrent.progress >= 100:
                    return True
                
                time.sleep(1)
            
            return False
            
        except Exception as e:
            log.error(f"Error esperando: {e}")
            return False

    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño"""
        if n < 0:
            n = 0
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.1f} {u}"
            n /= 1024
        return f"{n:.1f} PB"

    def close(self):
        """Cierra cliente"""
        log.info("🛑 Cerrando Transmission...")
        try:
            self.client.close()
        except:
            pass

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TorrentBot:
    """Bot Telegram con Transmission"""

    def __init__(self):
        self.config = Config()
        self.storage = Path(self.config.STORAGE_PATH)
        self.storage.mkdir(exist_ok=True)
        
        # Cliente Transmission
        self.transmission = TransmissionClient()
        
        # Control de tareas
        self.active_tasks: Dict[int, Dict] = {}  # chat_id -> {torrent_id, task}
        self.status_msgs: Dict[int, object] = {}
        
        # Cliente Telegram
        self.client = TelegramClient(
            "bot_session",
            self.config.API_ID,
            self.config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✅ Bot TeleTorrent v8.0 inicializado")

    async def start(self):
        """Inicia el bot"""
        try:
            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=self.config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot: @{me.username}")
            
            try:
                ch = await self.client.get_entity(self.config.CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except:
                log.warning("⚠ Canal no accesible")
            
            self._register_handlers()
            
            log.info("🔍 Escuchando mensajes...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error: {e}")

    def _register_handlers(self):
        """Registra handlers de eventos"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_handler(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v8.0*\n\n"
                "Descarga torrents con Transmission\n"
                "Sube a Telegram automáticamente\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar descarga\n\n"
                "*💡 Envía:*\n"
                "• Magnet link\n"
                "• Archivo .torrent\n"
                "• URL .torrent\n"
                "• Link HTTP/HTTPS",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_handler(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas activas* ⏸", parse_mode="markdown")
                return
            
            torrent_id = self.active_tasks[cid]["torrent_id"]
            p = self.transmission.get_progress(torrent_id)
            
            if not p:
                await event.reply("*Descarga no disponible* ❌", parse_mode="markdown")
                return
            
            bar = self._progress_bar(int(p["progress"]))
            speed_text = TransmissionClient.format_size(p["speed"])
            downloaded_text = TransmissionClient.format_size(p["downloaded"])
            total_text = TransmissionClient.format_size(p["total"])
            
            eta_text = f"{int(p['eta'])}s" if p["eta"] > 0 else "∞"
            peers_text = p["peers"]
            
            await event.reply(
                f"*⬇️ Descargando: {p['name'][:30]}*\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed_text}/s\n"
                f"📥 {downloaded_text} / {total_text}\n"
                f"⏱ ETA: {eta_text}\n"
                f"🌱 Peers: {peers_text}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_handler(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar* ⏹", parse_mode="markdown")
                return
            
            torrent_id = self.active_tasks[cid]["torrent_id"]
            
            # Eliminar torrent
            self.transmission.remove_torrent(torrent_id, delete_data=True)
            
            del self.active_tasks[cid]
            
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]
            
            await event.reply("*Descarga cancelada* ✓", parse_mode="markdown")

        @self.client.on(events.NewMessage)
        async def message_handler(event):
            text = (event.message.text or "").strip()
            
            # Ignorar comandos
            if text.startswith("/"):
                return
            
            # Detectar magnet link
            if text.startswith("magnet:?xt="):
                log.info(f"📌 Magnet detectado")
                await self._start_magnet_download(event, text)
                return
            
            # Detectar URL .torrent
            if text.endswith(".torrent"):
                log.info(f"📌 URL .torrent detectada")
                await self._download_torrent_url(event, text)
                return
            
            # Detectar URL HTTP/HTTPS
            if text.startswith(("http://", "https://", "ftp://")):
                log.info(f"📌 URL HTTP detectada")
                await self._start_http_download(event, text)
                return
            
            # Detectar archivo adjunto
            if event.message.document:
                log.info(f"📌 Archivo adjunto detectado")
                await self._handle_torrent_file(event)

    async def _handle_torrent_file(self, event):
        """Maneja archivo .torrent adjunto"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        doc = event.message.document
        filename = doc.file_name or "torrent.torrent"
        
        # Verificar que sea un torrent
        is_torrent = (
            "torrent" in (doc.mime_type or "").lower() or
            filename.lower().endswith(".torrent")
        )
        
        if not is_torrent:
            await event.reply("*❌ Solo archivos .torrent*", parse_mode="markdown")
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando archivo .torrent...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            log.info(f"📥 Descargando archivo: {filename}")
            file_data = await event.message.download_media(bytes)
            
            if not file_data or len(file_data) == 0:
                log.error("Archivo vacío")
                await sm.edit("*❌ Archivo vacío*")
                return
            
            log.info(f"📦 Agregando a Transmission: {len(file_data)} bytes")
            info = self.transmission.add_torrent_file(file_data, filename)
            
            self.active_tasks[cid] = {
                "torrent_id": info["torrent_id"],
                "name": info["name"],
                "task": None
            }
            
            await sm.edit(
                f"*✅ Torrent agregado:*\n"
                f"`{info['name'][:40]}`\n\n"
                f"`{self._progress_bar(0)}` 0%"
            )
            
            # Iniciar descarga
            self.transmission.start_torrent(info["torrent_id"])
            
            # Monitorear
            asyncio.create_task(self._monitor_torrent(cid))
            
        except Exception as e:
            log.error(f"Error: {e}", exc_info=True)
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")
            if cid in self.active_tasks:
                del self.active_tasks[cid]

    async def _start_magnet_download(self, event, magnet_uri: str):
        """Inicia descarga de magnet"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando magnet...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            log.info(f"📥 Agregando magnet")
            info = self.transmission.add_magnet(magnet_uri)
            
            self.active_tasks[cid] = {
                "torrent_id": info["torrent_id"],
                "name": info["name"],
                "task": None
            }
            
            await sm.edit(
                f"*✅ Magnet agregado:*\n"
                f"`{info['name'][:40]}`\n\n"
                f"`{self._progress_bar(0)}` 0%"
            )
            
            # Iniciar descarga
            self.transmission.start_torrent(info["torrent_id"])
            
            # Monitorear
            asyncio.create_task(self._monitor_torrent(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")

    async def _download_torrent_url(self, event, url: str):
        """Descarga .torrent desde URL"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Descargando archivo .torrent...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            log.info(f"📥 Descargando torrent desde URL: {url}")
            
            r = requests.get(url, timeout=30)
            if r.status_code != 200:
                await sm.edit(f"*❌ Error HTTP {r.status_code}*")
                return
            
            filename = url.split("/")[-1] or "torrent.torrent"
            
            log.info(f"📦 Agregando a Transmission")
            info = self.transmission.add_torrent_file(r.content, filename)
            
            self.active_tasks[cid] = {
                "torrent_id": info["torrent_id"],
                "name": info["name"],
                "task": None
            }
            
            await sm.edit(
                f"*✅ Torrent descargado:*\n"
                f"`{info['name'][:40]}`\n\n"
                f"`{self._progress_bar(0)}` 0%"
            )
            
            # Iniciar descarga
            self.transmission.start_torrent(info["torrent_id"])
            
            # Monitorear
            asyncio.create_task(self._monitor_torrent(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")

    async def _start_http_download(self, event, url: str):
        """Descarga archivo HTTP"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Iniciando descarga...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            filename = url.split("/")[-1] or "descarga"
            file_path = self.storage / filename
            
            # Descargar
            log.info(f"📥 Descargando: {url}")
            
            r = requests.get(url, timeout=60, stream=True)
            r.raise_for_status()
            
            total_size = int(r.headers.get("content-length", 0))
            
            downloaded = 0
            start_time = time.time()
            
            with open(file_path, "wb") as f:
                for chunk in r.iter_content(chunk_size=5*1024*1024):
                    if cid not in self.active_tasks:
                        file_path.unlink(missing_ok=True)
                        return
                    
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        
                        if total_size > 0:
                            progress = (downloaded / total_size) * 100
                            bar = self._progress_bar(int(progress))
                            
                            speed = downloaded / (time.time() - start_time)
                            speed_text = TransmissionClient.format_size(int(speed))
                            
                            try:
                                await sm.edit(
                                    f"*📥 Descargando: {filename[:30]}*\n\n"
                                    f"`{bar}` {progress:.1f}%\n"
                                    f"{speed_text}/s"
                                )
                            except:
                                pass
            
            self.active_tasks[cid] = {"torrent_id": -1, "name": filename}
            
            # Subir
            await self._upload_file(cid, file_path)
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")

    async def _monitor_torrent(self, cid: int):
        """Monitorea descarga del torrent"""
        try:
            if cid not in self.active_tasks:
                return
            
            torrent_id = self.active_tasks[cid]["torrent_id"]
            last_progress = -1
            
            while cid in self.active_tasks:
                p = self.transmission.get_progress(torrent_id)
                
                if not p:
                    await asyncio.sleep(1)
                    continue
                
                # Actualizar progreso
                if int(p["progress"]) != last_progress:
                    last_progress = int(p["progress"])
                    
                    bar = self._progress_bar(int(p["progress"]))
                    speed_text = TransmissionClient.format_size(int(p["speed"]))
                    downloaded_text = TransmissionClient.format_size(int(p["downloaded"]))
                    total_text = TransmissionClient.format_size(int(p["total"]))
                    
                    eta_text = f"{int(p['eta'])}s" if p["eta"] > 0 else "∞"
                    
                    try:
                        if cid in self.status_msgs:
                            await self.status_msgs[cid].edit(
                                f"*⬇️ Descargando: {p['name'][:30]}*\n\n"
                                f"`{bar}` `{p['progress']:.1f}%`\n"
                                f"📊 {speed_text}/s\n"
                                f"📥 {downloaded_text} / {total_text}\n"
                                f"⏱ ETA: {eta_text}"
                            )
                    except:
                        pass
                
                # Completado
                if p["progress"] >= 100:
                    log.info(f"✅ Descarga completada: {p['name']}")
                    break
                
                await asyncio.sleep(1)
            
            # Descarga completada - Subir archivos
            if cid in self.active_tasks:
                await self._msg(cid, "*✅ Descarga completada! Subiendo...*")
                
                files = self.transmission.get_files(torrent_id)
                if files:
                    await self._upload_files(cid, files)
                else:
                    await self._msg(cid, "*⚠ Sin archivos para subir*")
                
                # Eliminar torrent
                self.transmission.remove_torrent(torrent_id, delete_data=True)
                
        except Exception as e:
            log.error(f"Error monitor: {e}")
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

    async def _upload_files(self, cid: int, files: List[str]):
        """Sube múltiples archivos"""
        uploaded = 0
        failed = 0
        
        for file_path in files[:5]:
            try:
                if not os.path.exists(file_path):
                    continue
                
                file_size = os.path.getsize(file_path)
                if file_size == 0:
                    continue
                
                await self._upload_file(cid, Path(file_path))
                uploaded += 1
                
            except Exception as e:
                log.error(f"Error: {e}")
                failed += 1
            
            await asyncio.sleep(0.5)
        
        # Mensaje final
        if uploaded > 0:
            msg = f"*✅ Completado!*\n📦 {uploaded} archivo(s) subido(s)"
        else:
            msg = "*❌ Error en upload*"
        
        if failed > 0:
            msg = f"*⚠️ Parcial*\n✅ {uploaded} OK\n❌ {failed} falló"
        
        try:
            await self.client.send_message(cid, msg, parse_mode="markdown")
        except:
            pass

    async def _upload_file(self, cid: int, file_path: Path):
        """Sube un archivo a Telegram"""
        try:
            filename = file_path.name
            file_size = file_path.stat().st_size
            
            log.info(f"📤 Subiendo: {filename} ({TransmissionClient.format_size(file_size)})")
            
            await self._msg(cid, f"*📤 Subiendo:* `{filename[:30]}`")
            
            # Subir a Telegram
            await self.client.send_file(
                self.config.CHANNEL_ID,
                file=str(file_path),
                caption=filename,
                force_document=True
            )
            
            # Eliminar archivo local
            try:
                file_path.unlink()
                log.info(f"🗑️ Eliminado: {filename}")
            except Exception as e:
                log.warning(f"No se pudo eliminar: {e}")
            
            log.info(f"✅ Enviado: {filename}")
            
        except Exception as e:
            log.error(f"Error upload: {e}")
            raise

    async def _msg(self, cid: int, text: str):
        """Actualiza mensaje de estado"""
        if cid in self.status_msgs:
            try:
                await self.status_msgs[cid].edit(text)
            except:
                pass

    @staticmethod
    def _progress_bar(progress: int) -> str:
        """Crea barra de progreso"""
        filled = min(int(progress / 5), 20)
        empty = 20 - filled
        return "█" * filled + "░" * empty

    async def stop(self):
        """Detiene el bot"""
        log.info("🛑 Deteniendo...")
        self.transmission.close()
        await self.client.disconnect()
        log.info("✓ Detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    """Main async"""
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 70)
    log.info("🚀 TeleTorrent Bot v8.0 - CON TRANSMISSION")
    log.info("=" * 70)
    
    bot = TorrentBot()
    
    def signal_handler(signum, frame):
        log.info("Señal recibida, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main(bot))
    except Exception as e:
        log.error(f"Fatal: {e}")
