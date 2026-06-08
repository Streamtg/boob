#!/usr/bin/env python3
"""
TeleTorrent Bot v3.2 - Python (Usando Aria2)
Descarga torrents y archivos a través de Telegram
Credenciales integradas - CON DETECCIÓN MEJORADA DE TORRENTS
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
import argparse
from datetime import datetime
from pathlib import Path
from typing import Optional
from urllib.parse import quote

# ═══ IMPORTACIONES ═══════════════════════════════════════════════════════════
try:
    from telethon import TelegramClient, events
    from telethon.tl.types import DocumentAttributeFilename
except ImportError:
    print("❌ ERROR: pip install telethon")
    sys.exit(1)

try:
    import requests
except ImportError:
    print("❌ ERROR: pip install requests")
    sys.exit(1)

# ═══ CONFIGURACION INTEGRADA ═════════════════════════════════════════════════
class Config:
    """Configuración centralizada con credenciales"""
    
    # ⚠️ CREDENCIALES DE TELEGRAM
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    
    # 🔧 CONFIGURACION
    STORAGE_PATH = "./downloads"
    MAX_WORKERS = 3
    UPDATE_INTERVAL = 2
    CHUNK_SIZE = 5 * 1024 * 1024  # 5MB chunks
    
    # 🌐 ARIA2
    ARIA2_CONF = {
        "max-concurrent-downloads": MAX_WORKERS,
        "max-connection-per-server": 4,
        "split": 4,
        "min-split-size": 1024000,
        "summary-interval": 1,
    }

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ CLIENTE DE DESCARGA (ARIA2) ═════════════════════════════════════════════
class DownloadClient:
    """Cliente de descarga con aria2c"""

    def __init__(self, storage_path: str):
        self.storage_path = Path(storage_path)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        # Crear carpeta para torrents
        self.torrent_path = self.storage_path / "torrents"
        self.torrent_path.mkdir(exist_ok=True)
        
        self.active_downloads: dict = {}
        self.processes: dict = {}
        
        # Verificar si aria2c está instalado
        if not self._check_aria2():
            log.warning("⚠️  aria2c no encontrado. Usando descarga alternativa.")
            self.use_aria2 = False
        else:
            self.use_aria2 = True
            log.info("✓ aria2c disponible")
        
        log.info(f"✓ Cliente de descarga iniciado. Storage: {self.storage_path}")

    @staticmethod
    def _check_aria2() -> bool:
        """Verifica si aria2c está instalado"""
        try:
            result = subprocess.run(
                ["aria2c", "--version"],
                capture_output=True,
                timeout=5
            )
            return result.returncode == 0
        except (FileNotFoundError, subprocess.TimeoutExpired):
            return False

    def add_magnet(self, magnet_uri: str) -> dict:
        """Agrega un magnet link"""
        try:
            magnet_uri = magnet_uri.strip()
            if "&" in magnet_uri:
                magnet_uri = magnet_uri[:magnet_uri.index("&")]
            
            if not magnet_uri.startswith("magnet:?xt="):
                raise ValueError("Magnet link inválido")
            
            # Extraer nombre del magnet
            dn_match = re.search(r"dn=([^&]+)", magnet_uri)
            name = dn_match.group(1) if dn_match else "descarga"
            try:
                name = quote(name, safe="")
            except:
                pass
            
            download_id = hashlib.md5(magnet_uri.encode()).hexdigest()[:8]
            
            self.active_downloads[download_id] = {
                "type": "magnet",
                "uri": magnet_uri,
                "name": name,
                "progress": 0,
                "downloaded": 0,
                "total": 0,
                "speed": 0,
                "status": "pending",
                "started_at": time.time(),
            }
            
            log.info(f"✓ Magnet agregado: {name}")
            
            return {
                "download_id": download_id,
                "name": name,
                "uri": magnet_uri,
            }
            
        except Exception as e:
            log.error(f"Error agregando magnet: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes, name: str) -> dict:
        """Agrega un archivo torrent"""
        try:
            # Limpiar nombre
            name = name.replace(".torrent", "").strip()
            if not name:
                name = f"torrent_{int(time.time())}"
            
            # Guardar archivo torrent
            safe_name = re.sub(r'[^a-zA-Z0-9_-]', '_', name)
            torrent_path = self.torrent_path / f"{safe_name}.torrent"
            
            # Si ya existe, agregar timestamp
            if torrent_path.exists():
                torrent_path = self.torrent_path / f"{safe_name}_{int(time.time())}.torrent"
            
            torrent_path.write_bytes(torrent_data)
            
            download_id = hashlib.md5(torrent_data).hexdigest()[:8]
            
            self.active_downloads[download_id] = {
                "type": "torrent",
                "path": str(torrent_path),
                "name": name,
                "progress": 0,
                "downloaded": 0,
                "total": 0,
                "speed": 0,
                "status": "pending",
                "started_at": time.time(),
            }
            
            log.info(f"✓ Torrent agregado: {name} (archivo: {torrent_path.name})")
            
            return {
                "download_id": download_id,
                "name": name,
                "type": "torrent",
            }
            
        except Exception as e:
            log.error(f"Error agregando torrent: {e}")
            raise

    def add_http(self, url: str, name: str) -> dict:
        """Agrega una descarga HTTP"""
        try:
            # Limpiar nombre
            name = name.split("?")[0].split("#")[0]
            if not name or name.endswith("/"):
                name = "descarga"
            
            # Limitar longitud del nombre
            name = name[-100:]  # Últimos 100 caracteres
            
            download_id = hashlib.md5(url.encode()).hexdigest()[:8]
            
            self.active_downloads[download_id] = {
                "type": "http",
                "uri": url,
                "name": name,
                "progress": 0,
                "downloaded": 0,
                "total": 0,
                "speed": 0,
                "status": "pending",
                "started_at": time.time(),
            }
            
            log.info(f"✓ URL HTTP agregada: {name}")
            
            return {
                "download_id": download_id,
                "name": name,
                "type": "http",
            }
            
        except Exception as e:
            log.error(f"Error agregando HTTP: {e}")
            raise

    def get_progress(self, download_id: str) -> Optional[dict]:
        """Obtiene el progreso de una descarga"""
        if download_id not in self.active_downloads:
            return None
        
        d = self.active_downloads[download_id]
        
        return {
            "progress": d["progress"],
            "downloaded": d["downloaded"],
            "total": d["total"],
            "speed": d["speed"],
            "status": d["status"],
            "elapsed": time.time() - d["started_at"],
        }

    def cancel(self, download_id: str):
        """Cancela una descarga"""
        if download_id in self.active_downloads:
            d = self.active_downloads[download_id]
            
            if download_id in self.processes:
                proc = self.processes[download_id]
                try:
                    proc.terminate()
                    proc.wait(timeout=5)
                except:
                    try:
                        proc.kill()
                    except:
                        pass
                if download_id in self.processes:
                    del self.processes[download_id]
            
            d["status"] = "cancelled"
            log.info(f"✗ Cancelado: {d['name']}")

    def start_download(self, download_id: str):
        """Inicia una descarga"""
        if download_id not in self.active_downloads:
            return False
        
        d = self.active_downloads[download_id]
        
        try:
            if d["type"] == "http":
                # Descarga HTTP de forma sincrónica en thread
                import threading
                thread = threading.Thread(
                    target=self._download_http_sync,
                    args=(download_id,),
                    daemon=True
                )
                thread.start()
            elif self.use_aria2:
                # aria2c en thread
                import threading
                thread = threading.Thread(
                    target=self._download_aria2_sync,
                    args=(download_id,),
                    daemon=True
                )
                thread.start()
            else:
                # Fallback HTTP
                import threading
                thread = threading.Thread(
                    target=self._download_http_sync,
                    args=(download_id,),
                    daemon=True
                )
                thread.start()
            
            d["status"] = "downloading"
            return True
            
        except Exception as e:
            log.error(f"Error iniciando descarga: {e}")
            d["status"] = "error"
            return False

    def _download_http_sync(self, download_id: str):
        """Descarga HTTP sincrónica"""
        if download_id not in self.active_downloads:
            return
        
        d = self.active_downloads[download_id]
        url = d["uri"]
        name = d["name"]
        file_path = self.storage_path / name
        
        try:
            # Obtener tamaño total
            try:
                r = requests.head(url, timeout=10, allow_redirects=True)
                total_size = int(r.headers.get("content-length", 0))
            except:
                total_size = 0
            
            if total_size > 0:
                d["total"] = total_size
            
            # Descargar
            r = requests.get(url, timeout=60, stream=True, allow_redirects=True)
            r.raise_for_status()
            
            downloaded = 0
            start_time = time.time()
            
            with open(file_path, "wb") as f:
                for chunk in r.iter_content(chunk_size=Config.CHUNK_SIZE):
                    if d["status"] == "cancelled":
                        file_path.unlink(missing_ok=True)
                        return
                    
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        d["downloaded"] = downloaded
                        
                        if total_size > 0:
                            d["progress"] = min(100, (downloaded / total_size) * 100)
                        
                        elapsed = time.time() - start_time
                        if elapsed > 0:
                            d["speed"] = downloaded / elapsed
            
            d["status"] = "completed"
            d["progress"] = 100
            log.info(f"✅ Completado: {name}")
            
        except Exception as e:
            log.error(f"Error descargando {name}: {e}")
            d["status"] = "error"
            try:
                file_path.unlink(missing_ok=True)
            except:
                pass

    def _download_aria2_sync(self, download_id: str):
        """Descarga con aria2c sincrónica"""
        if download_id not in self.active_downloads:
            return
        
        d = self.active_downloads[download_id]
        
        try:
            cmd = [
                "aria2c",
                "--max-concurrent-downloads=1",
                "--max-connection-per-server=4",
                "--split=4",
                f"--dir={self.storage_path}",
                "--continue=true",
                "--summary-interval=1",
                "--quiet=false",
            ]
            
            if d["type"] == "magnet":
                cmd.append(d["uri"])
                log.info(f"🚀 Iniciando descarga de magnet: {d['name']}")
            elif d["type"] == "torrent":
                torrent_file = d["path"]
                if not os.path.exists(torrent_file):
                    log.error(f"Archivo torrent no encontrado: {torrent_file}")
                    d["status"] = "error"
                    return
                
                cmd.append(torrent_file)
                log.info(f"🚀 Iniciando descarga de torrent: {d['name']} ({torrent_file})")
            
            log.info(f"📡 Ejecutando: {' '.join(cmd)}")
            
            proc = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                bufsize=1
            )
            
            self.processes[download_id] = proc
            d["status"] = "downloading"
            
            # Leer output en tiempo real
            import threading
            
            def read_output():
                try:
                    for line in proc.stdout:
                        if line and d["status"] == "downloading":
                            log.debug(f"[aria2] {line.strip()}")
                            # Intentar extraer progreso del output
                            if "%" in line:
                                try:
                                    # Buscar porcentaje
                                    match = re.search(r"(\d+)%", line)
                                    if match:
                                        d["progress"] = float(match.group(1))
                                except:
                                    pass
                except:
                    pass
            
            thread = threading.Thread(target=read_output, daemon=True)
            thread.start()
            
            # Esperar a que termine
            stdout, stderr = proc.communicate()
            
            if proc.returncode == 0:
                d["status"] = "completed"
                d["progress"] = 100
                log.info(f"✅ Completado: {d['name']}")
            else:
                d["status"] = "error"
                log.error(f"Error aria2 (código {proc.returncode}): {stderr[:200]}")
            
            if download_id in self.processes:
                del self.processes[download_id]
                
        except Exception as e:
            log.error(f"Error aria2: {e}")
            d["status"] = "error"
            if download_id in self.processes:
                del self.processes[download_id]

    def get_completed_files(self, download_id: str) -> list:
        """Obtiene los archivos completados"""
        if download_id not in self.active_downloads:
            return []
        
        d = self.active_downloads[download_id]
        files = []
        
        # Buscar archivos descargados
        try:
            for item in self.storage_path.rglob("*"):
                if item.is_file() and item.stat().st_size > 0:
                    # Evitar archivos de torrent y caché
                    if not item.name.endswith(".torrent") and not item.name.startswith("."):
                        files.append(str(item))
        except:
            pass
        
        return files[:10]  # Máximo 10 archivos

    @staticmethod
    def format_size(n: int) -> str:
        """Formatea tamaño en bytes"""
        if n < 0:
            n = 0
        for u in ("B", "KB", "MB", "GB", "TB"):
            if n < 1024:
                return f"{n:.1f} {u}"
            n /= 1024
        return f"{n:.1f} PB"

    def close(self):
        """Cierra el cliente"""
        for pid, proc in list(self.processes.items()):
            try:
                proc.terminate()
            except:
                pass

# ═══ BOT DE TELEGRAM ═════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot principal con credenciales integradas"""

    def __init__(self):
        self.config = Config()
        self.storage_path = Path(self.config.STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()
        
        self.download_client = DownloadClient(str(self.storage_path))
        self.active_tasks: dict = {}
        self.status_messages: dict = {}
        
        # Cliente Telegram con credenciales integradas
        self.client = TelegramClient(
            "teletorrent_session",
            self.config.API_ID,
            self.config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✓ Bot inicializado con credenciales")

    def _load_cache(self) -> dict:
        """Carga caché"""
        if self.cache_file.exists():
            try:
                return json.loads(self.cache_file.read_text())
            except Exception as e:
                log.warning(f"Error cargando cache: {e}")
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
        except Exception as e:
            log.warning(f"Error obteniendo file_id: {e}")
        
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
        except Exception as e:
            log.warning(f"Error cacheando file_id: {e}")

    async def start(self):
        """Inicia el bot"""
        try:
            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=self.config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot activo: @{me.username} (ID: {me.id})")
            
            try:
                ch = await self.client.get_entity(self.config.CHANNEL_ID)
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
        """Registra manejadores de eventos"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_h(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v3.2*\n\n"
                "Descarga torrents y archivos a Telegram\n"
                "sin límites de 50MB.\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar descarga\n"
                "🔹 `/cache` - Estado de caché\n\n"
                "*💡 Uso:*\n"
                "Envía:\n"
                "• Magnet link\n"
                "• URL .torrent\n"
                "• Link HTTP/HTTPS\n"
                "• Archivo .torrent (adjuntado)",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_h(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas activas* ⏸", parse_mode="markdown")
                return
            
            download_id = self.active_tasks[cid]
            p = self.download_client.get_progress(download_id)
            
            if not p:
                await event.reply("*Descarga no disponible* ❌", parse_mode="markdown")
                return
            
            bar = self._pbar(int(p["progress"]))
            speed_text = DownloadClient.format_size(p["speed"])
            downloaded_text = DownloadClient.format_size(p["downloaded"])
            total_text = DownloadClient.format_size(p["total"])
            
            await event.reply(
                f"*⬇️ Descargando...*\n\n"
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
            
            download_id = self.active_tasks[cid]
            self.download_client.cancel(download_id)
            del self.active_tasks[cid]
            
            if cid in self.status_messages:
                try:
                    await self.status_messages[cid].delete()
                except:
                    pass
                if cid in self.status_messages:
                    del self.status_messages[cid]
            
            await event.reply("*Cancelado* ✓", parse_mode="markdown")

        @self.client.on(events.NewMessage(pattern=r"^/cache$"))
        async def cache_h(event):
            count = len(self.file_cache)
            size = 0
            if self.cache_file.exists():
                try:
                    size = os.path.getsize(self.cache_file)
                except:
                    pass
            
            await event.reply(
                f"*📦 Caché:* {count} archivos\n"
                f"*💾 Tamaño:* {DownloadClient.format_size(size)}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage)
        async def msg_h(event):
            # Manejo de texto
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            # Detectar magnet, URL torrent, URL HTTP
            if text.startswith("magnet:"):
                log.info(f"📌 Detectado magnet link")
                await self._start_download(event, text)
                return
            elif text.endswith(".torrent"):
                log.info(f"📌 Detectada URL .torrent")
                await self._start_download(event, text)
                return
            elif text.startswith(("http://", "https://", "ftp://")):
                log.info(f"📌 Detectada URL")
                await self._start_download(event, text)
                return
            
            # Manejo de archivos adjuntos
            if event.message.document:
                log.info(f"📌 Detectado archivo adjunto")
                await self._handle_torrent_file(event)
                return

    async def _handle_torrent_file(self, event):
        """Maneja archivos .torrent adjuntos"""
        if not event.message.document:
            log.warning("No hay documento en el mensaje")
            return
        
        cid = event.chat_id
        doc = event.message.document
        file_name = doc.file_name or "descarga.torrent"
        
        log.info(f"Procesando archivo: {file_name} (tipo: {doc.mime_type})")
        
        # Verificar que sea un archivo torrent
        is_torrent = (
            "torrent" in (doc.mime_type or "").lower() or 
            file_name.lower().endswith(".torrent")
        )
        
        if not is_torrent:
            log.warning(f"Archivo no es torrent: {doc.mime_type}")
            await event.reply("*❌ Solo archivos .torrent*", parse_mode="markdown")
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando archivo .torrent...*", parse_mode="markdown")
        self.status_messages[cid] = sm
        
        try:
            log.info(f"Descargando contenido del archivo {file_name}")
            file_data = await event.message.download_media(bytes)
            
            if not file_data or len(file_data) == 0:
                log.error("Archivo vacío")
                await sm.edit("*❌ Archivo vacío*")
                return
            
            log.info(f"Tamaño del archivo: {len(file_data)} bytes")
            
            info = self.download_client.add_torrent_file(file_data, file_name)
            
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ Agregado:* `{info['name'][:40]}`\n\n"
                f"`{self._pbar(0)}` 0%"
            )
            
            # Iniciar descarga
            log.info(f"Iniciando descarga del torrent: {info['download_id']}")
            self.download_client.start_download(info["download_id"])
            
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error procesando torrent: {e}", exc_info=True)
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")
            if cid in self.active_tasks:
                del self.active_tasks[cid]

    async def _start_download(self, event, text):
        """Inicia una descarga desde URL o magnet"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa* 🔄", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando...*", parse_mode="markdown")
        self.status_messages[cid] = sm
        
        try:
            if text.startswith("magnet:"):
                log.info(f"Procesando magnet link")
                info = self.download_client.add_magnet(text)
                await sm.edit(f"*📥 Magnet:* `{info['name'][:40]}`")
                
            elif text.endswith(".torrent"):
                log.info(f"Descargando archivo torrent desde URL: {text}")
                await sm.edit("*📥 Descargando archivo .torrent...*")
                try:
                    r = requests.get(text, timeout=30)
                    if r.status_code != 200:
                        await sm.edit(f"*❌ Error HTTP {r.status_code}*")
                        if cid in self.active_tasks:
                            del self.active_tasks[cid]
                        return
                    
                    filename = text.split("/")[-1] or "descarga.torrent"
                    log.info(f"Torrent descargado: {len(r.content)} bytes")
                    info = self.download_client.add_torrent_file(r.content, filename)
                except Exception as e:
                    log.error(f"Error descargando URL .torrent: {e}")
                    await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")
                    if cid in self.active_tasks:
                        del self.active_tasks[cid]
                    return
                
            else:
                # HTTP directo
                log.info(f"Procesando URL HTTP: {text}")
                try:
                    r = requests.head(text, timeout=10, allow_redirects=True)
                    filename = text.split("/")[-1] or "descarga"
                except:
                    filename = "descarga"
                
                info = self.download_client.add_http(text, filename)
                await sm.edit(f"*📥 HTTP:* `{filename[:40]}`")
            
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ Agregado:*\n`{info['name'][:40]}`\n\n"
                f"`{self._pbar(0)}` 0%"
            )
            
            # Iniciar descarga
            log.info(f"Iniciando descarga: {info['download_id']}")
            self.download_client.start_download(info["download_id"])
            
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error iniciando descarga: {e}", exc_info=True)
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")
            if cid in self.active_tasks:
                del self.active_tasks[cid]

    async def _monitor(self, cid, download_id):
        """Monitorea descarga"""
        try:
            last_progress = -1
            check_count = 0
            
            while cid in self.active_tasks:
                p = self.download_client.get_progress(download_id)
                
                if not p:
                    check_count += 1
                    if check_count > 60:  # 60 segundos
                        break
                    await asyncio.sleep(1)
                    continue
                
                check_count = 0
                
                if int(p["progress"]) != last_progress:
                    last_progress = int(p["progress"])
                    await self._upd_progress(cid, p)
                
                if p["status"] == "completed":
                    log.info(f"Descarga completada: {download_id}")
                    break
                elif p["status"] in ["error", "cancelled"]:
                    log.info(f"Descarga {p['status']}: {download_id}")
                    break
                
                await asyncio.sleep(1)
            
            await self._upd_msg(cid, "*✅ Descarga completa! Subiendo...*")
            
            files = self.download_client.get_completed_files(download_id)
            if files:
                log.info(f"Archivos encontrados: {len(files)}")
                await self._upload(cid, files)
            else:
                log.warning(f"Sin archivos para subir")
                await self._upd_msg(cid, "*⚠ Sin archivos para subir*")
                
        except Exception as e:
            log.error(f"Error monitor: {e}", exc_info=True)
            await self._upd_msg(cid, f"*❌ Error:* `{str(e)[:80]}`")
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
        """Actualiza progreso"""
        if cid not in self.status_messages:
            return
        
        bar = self._pbar(int(p["progress"]))
        speed = DownloadClient.format_size(p["speed"])
        downloaded = DownloadClient.format_size(p["downloaded"])
        total = DownloadClient.format_size(p["total"])
        
        try:
            await self.status_messages[cid].edit(
                f"*⬇️ Descargando...*\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed}/s\n"
                f"📥 {downloaded} / {total}\n"
                f"⏱ {int(p['elapsed'])}s",
                parse_mode="markdown"
            )
        except:
            pass

    async def _upd_msg(self, cid, text):
        """Actualiza mensaje"""
        if cid in self.status_messages:
            try:
                await self.status_messages[cid].edit(text)
            except:
                pass

    @staticmethod
    def _pbar(p: int) -> str:
        """Barra de progreso"""
        filled = min(int(p / 5), 20)
        return "█" * filled + "░" * (20 - filled)

    async def _upload(self, cid, files):
        """Sube archivos al canal"""
        uploaded = 0
        failed = 0
        
        for file_path in files[:5]:  # Máximo 5 archivos
            if not os.path.exists(file_path):
                continue
            
            try:
                file_size = os.path.getsize(file_path)
            except:
                continue
            
            if file_size == 0:
                continue
            
            file_name = os.path.basename(file_path)
            
            try:
                await self._upd_msg(cid, f"*📤 Subiendo:* `{file_name[:30]}`")
                
                response = await self.client.send_file(
                    self.config.CHANNEL_ID,
                    file=file_path,
                    caption=file_name,
                    force_document=True
                )
                
                if response and hasattr(response, "media"):
                    if hasattr(response.media, "document"):
                        self._cache_file_id(file_path, str(response.media.document.id))
                
                uploaded += 1
                log.info(f"✅ Enviado: {file_name}")
                
            except Exception as e:
                log.error(f"Error upload {file_name}: {e}")
                failed += 1
            
            await asyncio.sleep(0.5)
        
        msg = f"*✅ Completado!*\n📦 {uploaded} archivo(s)"
        if failed > 0:
            msg = f"*⚠️ Parcial*\n✅ {uploaded} OK\n❌ {failed} falló"
        
        try:
            await self.client.send_message(cid, msg, parse_mode="markdown")
        except:
            pass

    async def stop(self):
        """Detiene bot"""
        log.info("🛑 Deteniendo...")
        self.download_client.close()
        await self.client.disconnect()
        log.info("✓ Detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main_async(bot):
    """Main async"""
    try:
        await bot.start()
    except KeyboardInterrupt:
        log.info("Interrupción del usuario")
    except Exception as e:
        log.error(f"Error: {e}")
    finally:
        await bot.stop()

def main():
    """Main"""
    log.info("=" * 60)
    log.info("🚀 TeleTorrent Bot v3.2 - Iniciando")
    log.info("=" * 60)
    log.info(f"📌 Credenciales integradas")
    log.info(f"🔧 Usando cliente de descarga automático")
    
    bot = TeleTorrentBot()
    
    def signal_handler(signum, frame):
        log.info("🛑 Señal recibida, cerrando...")
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main_async(bot))
    except Exception as e:
        log.error(f"Fatal: {e}")

if __name__ == "__main__":
    main()
