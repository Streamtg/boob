#!/usr/bin/env python3
"""
TeleTorrent Bot v3.3 - Python (Torrents REALES)
Descarga torrents con metadata y archivos a través de Telegram
Credenciales integradas
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
    MAX_WORKERS = 2
    UPDATE_INTERVAL = 2
    CHUNK_SIZE = 5 * 1024 * 1024  # 5MB chunks

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.DEBUG,  # DEBUG para ver todo
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ CLIENTE DE DESCARGA (ARIA2) ═════════════════════════════════════════════
class DownloadClient:
    """Cliente de descarga con aria2c con soporte REAL para torrents"""

    def __init__(self, storage_path: str):
        self.storage_path = Path(storage_path)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        # Crear carpeta para torrents
        self.torrent_path = self.storage_path / "torrents"
        self.torrent_path.mkdir(exist_ok=True)
        
        # Crear carpeta de estado aria2
        self.aria2_state_dir = self.storage_path / ".aria2"
        self.aria2_state_dir.mkdir(exist_ok=True)
        
        self.active_downloads: dict = {}
        self.processes: dict = {}
        
        # Verificar si aria2c está instalado
        if not self._check_aria2():
            log.error("❌ aria2c NO encontrado. Instálalo con: sudo apt-get install -y aria2")
            sys.exit(1)
        else:
            self.use_aria2 = True
            log.info("✅ aria2c disponible y listo")
        
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
            if result.returncode == 0:
                version = result.stdout.decode().split('\n')[0]
                log.info(f"📌 {version}")
                return True
            return False
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
            name = dn_match.group(1) if dn_match else "magnet_download"
            try:
                from urllib.parse import unquote
                name = unquote(name)
            except:
                pass
            
            # Limpiar nombre
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)[:50]
            
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
        """Agrega un archivo torrent y extrae metadata"""
        try:
            # Limpiar nombre
            name = name.replace(".torrent", "").strip()
            if not name:
                name = f"torrent_{int(time.time())}"
            
            # Limpiar nombre
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)[:50]
            
            # Guardar archivo torrent
            torrent_path = self.torrent_path / f"{name}.torrent"
            
            # Si ya existe, agregar timestamp
            if torrent_path.exists():
                torrent_path = self.torrent_path / f"{name}_{int(time.time())}.torrent"
            
            torrent_path.write_bytes(torrent_data)
            log.info(f"📁 Archivo torrent guardado: {torrent_path}")
            
            # Extraer metadata del torrent
            torrent_info = self._parse_torrent(torrent_data)
            if torrent_info:
                display_name = torrent_info.get("name", name)
                total_size = torrent_info.get("size", 0)
                log.info(f"📋 Metadata del torrent: {display_name} ({self.format_size(total_size)})")
            else:
                display_name = name
                total_size = 0
            
            download_id = hashlib.md5(torrent_data).hexdigest()[:8]
            
            self.active_downloads[download_id] = {
                "type": "torrent",
                "path": str(torrent_path),
                "name": display_name,
                "progress": 0,
                "downloaded": 0,
                "total": total_size,
                "speed": 0,
                "status": "pending",
                "started_at": time.time(),
            }
            
            log.info(f"✓ Torrent agregado: {display_name}")
            
            return {
                "download_id": download_id,
                "name": display_name,
                "type": "torrent",
            }
            
        except Exception as e:
            log.error(f"Error agregando torrent: {e}", exc_info=True)
            raise

    @staticmethod
    def _parse_torrent(torrent_data: bytes) -> dict:
        """Extrae información básica del archivo torrent"""
        try:
            import bencode
        except ImportError:
            log.warning("⚠️ bencode no instalado, instalando...")
            subprocess.run(["pip", "install", "bencode.py"], capture_output=True)
            try:
                import bencode
            except:
                log.warning("⚠️ No se pudo instalar bencode")
                return {}
        
        try:
            decoded = bencode.decode(torrent_data)
            
            # Extraer información
            info = decoded.get(b'info', {})
            name = info.get(b'name', b'Unknown').decode('utf-8', errors='ignore')
            
            # Calcular tamaño
            if b'files' in info:
                total_size = sum(f.get(b'length', 0) for f in info.get(b'files', []))
            else:
                total_size = info.get(b'length', 0)
            
            log.info(f"📊 Metadata extraída: {name} - {total_size} bytes")
            
            return {
                "name": name,
                "size": total_size,
            }
        except Exception as e:
            log.warning(f"No se pudo parsear torrent: {e}")
            return {}

    def add_http(self, url: str, name: str) -> dict:
        """Agrega una descarga HTTP"""
        try:
            # Limpiar nombre
            name = name.split("?")[0].split("#")[0]
            if not name or name.endswith("/"):
                name = "descarga"
            
            # Limitar longitud del nombre
            name = name[-100:]
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)
            
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
                import threading
                thread = threading.Thread(
                    target=self._download_http_sync,
                    args=(download_id,),
                    daemon=True
                )
                thread.start()
            else:
                # Para magnet y torrent usar aria2c
                import threading
                thread = threading.Thread(
                    target=self._download_aria2_sync,
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
        """Descarga con aria2c sincrónica - VERSIÓN COMPLETA"""
        if download_id not in self.active_downloads:
            return
        
        d = self.active_downloads[download_id]
        
        try:
            # Configuración COMPLETA de aria2c para torrents
            cmd = [
                "aria2c",
                # Descargas concurrentes
                f"--max-concurrent-downloads=1",
                f"--max-connection-per-server=4",
                f"--split=4",
                
                # Directorio de descarga
                f"--dir={self.storage_path}",
                
                # Configuración de torrents
                "--enable-dht=true",
                "--enable-dht6=true",
                "--dht-listen-port=6881-6889",
                "--enable-peer-exchange=true",
                "--bt-tracker-connect-timeout=10",
                "--bt-request-timeout=10",
                "--bt-max-peers=100",
                "--min-tls-version=TLSv1_2",
                
                # Descarga reanudable
                "--continue=true",
                "--allow-overwrite=true",
                
                # Output
                "--summary-interval=1",
                "--console-log-level=debug",
                f"--save-session={self.aria2_state_dir}/aria2.session",
                f"--input-file={self.aria2_state_dir}/aria2.session",
                
                # RPC (para monitoreo - opcional)
                "--enable-rpc=false",
            ]
            
            if d["type"] == "magnet":
                log.info(f"🚀 Iniciando descarga MAGNET: {d['name']}")
                cmd.append(d["uri"])
                
            elif d["type"] == "torrent":
                torrent_file = d["path"]
                if not os.path.exists(torrent_file):
                    log.error(f"❌ Archivo torrent no encontrado: {torrent_file}")
                    d["status"] = "error"
                    return
                
                log.info(f"🚀 Iniciando descarga TORRENT: {d['name']}")
                log.info(f"📁 Archivo: {torrent_file}")
                cmd.append(torrent_file)
            
            log.info(f"📡 Ejecutando aria2c...")
            log.debug(f"Comando: {' '.join(cmd)}")
            
            # Ejecutar aria2c
            proc = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True,
                bufsize=1
            )
            
            self.processes[download_id] = proc
            d["status"] = "downloading"
            
            # Monitorear output
            import threading
            
            def read_output(pipe, prefix):
                try:
                    for line in iter(pipe.readline, ''):
                        if line:
                            log.debug(f"[aria2 {prefix}] {line.strip()}")
                            
                            if d["status"] == "downloading":
                                # Buscar información de progreso
                                if "%" in line:
                                    try:
                                        match = re.search(r"(\d+(?:\.\d+)?)%", line)
                                        if match:
                                            d["progress"] = float(match.group(1))
                                    except:
                                        pass
                                
                                # Velocidad
                                if "B/s" in line or "KB/s" in line or "MB/s" in line:
                                    try:
                                        match = re.search(r"([\d.]+)\s*([KMGT]?)B/s", line)
                                        if match:
                                            speed = float(match.group(1))
                                            unit = match.group(2) or ""
                                            multipliers = {'K': 1024, 'M': 1024**2, 'G': 1024**3, 'T': 1024**4}
                                            d["speed"] = speed * multipliers.get(unit, 1)
                                    except:
                                        pass
                except:
                    pass
            
            # Leer stdout y stderr en threads separados
            stdout_thread = threading.Thread(
                target=read_output,
                args=(proc.stdout, "stdout"),
                daemon=True
            )
            stderr_thread = threading.Thread(
                target=read_output,
                args=(proc.stderr, "stderr"),
                daemon=True
            )
            stdout_thread.start()
            stderr_thread.start()
            
            # Esperar a que termine
            log.info(f"⏳ Esperando descarga de {d['name']}...")
            returncode = proc.wait()
            
            log.info(f"✋ Proceso aria2c terminado con código: {returncode}")
            
            if returncode == 0:
                d["status"] = "completed"
                d["progress"] = 100
                log.info(f"✅ COMPLETADO: {d['name']}")
            else:
                d["status"] = "error"
                log.error(f"❌ Error en aria2c (código {returncode})")
            
            if download_id in self.processes:
                del self.processes[download_id]
                
        except Exception as e:
            log.error(f"❌ Error aria2: {e}", exc_info=True)
            d["status"] = "error"
            if download_id in self.processes:
                del self.processes[download_id]

    def get_completed_files(self, download_id: str) -> list:
        """Obtiene los archivos completados"""
        if download_id not in self.active_downloads:
            return []
        
        d = self.active_downloads[download_id]
        files = []
        
        try:
            # Buscar archivos descargados (excluyendo torrent files y metadata)
            for item in self.storage_path.rglob("*"):
                if item.is_file() and item.stat().st_size > 0:
                    # Evitar archivos de torrent, caché y session
                    if (not item.name.endswith(".torrent") and 
                        not item.name.startswith(".") and
                        not str(item).startswith(str(self.aria2_state_dir)) and
                        not str(item).startswith(str(self.torrent_path))):
                        files.append(str(item))
        except:
            pass
        
        log.info(f"📦 Archivos encontrados: {len(files)}")
        return files[:10]

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
        log.info("🛑 Cerrando procesos aria2...")
        for pid, proc in list(self.processes.items()):
            try:
                proc.terminate()
                proc.wait(timeout=5)
            except:
                try:
                    proc.kill()
                except:
                    pass

# ═══ BOT DE TELEGRAM ═════════════════════════════════════════════════════════
class TeleTorrentBot:
    """Bot principal"""

    def __init__(self):
        self.config = Config()
        self.storage_path = Path(self.config.STORAGE_PATH)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.cache_file = self.storage_path / "cache.json"
        self.file_cache = self._load_cache()
        
        self.download_client = DownloadClient(str(self.storage_path))
        self.active_tasks: dict = {}
        self.status_messages: dict = {}
        
        self.client = TelegramClient(
            "teletorrent_session",
            self.config.API_ID,
            self.config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✓ Bot inicializado")

    def _load_cache(self) -> dict:
        """Carga caché"""
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
        except:
            pass

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
            await self.client.start(bot_token=self.config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot activo: @{me.username} (ID: {me.id})")
            
            try:
                ch = await self.client.get_entity(self.config.CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except:
                log.warning(f"⚠ Canal no accesible")

            self._register_handlers()
            
            log.info("🔍 Escuchando mensajes...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error iniciando bot: {e}", exc_info=True)
            raise

    def _register_handlers(self):
        """Registra manejadores de eventos"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_h(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v3.3*\n\n"
                "Descarga torrents REALES y archivos\n\n"
                "*📋 Comandos:*\n"
                "🔹 `/help` - Esta ayuda\n"
                "🔹 `/status` - Ver progreso\n"
                "🔹 `/cancel` - Cancelar\n\n"
                "*💡 Envía:*\n"
                "• Magnet link\n"
                "• Archivo .torrent\n"
                "• URL .torrent\n"
                "• Link HTTP",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_h(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas* ⏸", parse_mode="markdown")
                return
            
            download_id = self.active_tasks[cid]
            p = self.download_client.get_progress(download_id)
            
            if not p:
                await event.reply("*No disponible* ❌", parse_mode="markdown")
                return
            
            bar = self._pbar(int(p["progress"]))
            speed = DownloadClient.format_size(p["speed"])
            down = DownloadClient.format_size(p["downloaded"])
            total = DownloadClient.format_size(p["total"])
            
            await event.reply(
                f"*⬇️ {p['status'].upper()}*\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed}/s\n"
                f"📥 {down} / {total}",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_h(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar*", parse_mode="markdown")
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

        @self.client.on(events.NewMessage)
        async def msg_h(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            # Detectar URLs y magnets
            if text.startswith(("magnet:", "http://", "https://", "ftp://")):
                log.info(f"📌 URL detectada: {text[:50]}")
                await self._start_download(event, text)
                return
            
            # Detectar archivo adjunto
            if event.message.document:
                log.info(f"📌 Archivo adjunto detectado")
                await self._handle_torrent_file(event)
                return

    async def _handle_torrent_file(self, event):
        """Maneja archivos .torrent adjuntos"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        doc = event.message.document
        file_name = doc.file_name or "descarga.torrent"
        
        log.info(f"📥 Procesando archivo: {file_name}")
        
        # Verificar que sea un archivo torrent
        is_torrent = (
            "torrent" in (doc.mime_type or "").lower() or 
            file_name.lower().endswith(".torrent")
        )
        
        if not is_torrent:
            log.warning(f"Archivo no es torrent")
            await event.reply("*❌ Solo archivos .torrent*", parse_mode="markdown")
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando torrent...*", parse_mode="markdown")
        self.status_messages[cid] = sm
        
        try:
            log.info(f"Descargando contenido del archivo")
            file_data = await event.message.download_media(bytes)
            
            if not file_data or len(file_data) == 0:
                log.error("Archivo vacío")
                await sm.edit("*❌ Archivo vacío*")
                return
            
            log.info(f"Tamaño: {len(file_data)} bytes")
            
            info = self.download_client.add_torrent_file(file_data, file_name)
            
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ Agregado:*\n`{info['name'][:40]}`\n\n"
                f"`{self._pbar(0)}` 0%"
            )
            
            log.info(f"Iniciando descarga: {info['download_id']}")
            self.download_client.start_download(info["download_id"])
            
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error: {e}", exc_info=True)
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")

    async def _start_download(self, event, text):
        """Inicia descarga"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga activa*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando...*", parse_mode="markdown")
        self.status_messages[cid] = sm
        
        try:
            if text.startswith("magnet:"):
                log.info(f"Procesando magnet")
                info = self.download_client.add_magnet(text)
                await sm.edit(f"*📥 Magnet*")
                
            elif text.endswith(".torrent"):
                log.info(f"Descargando torrent URL")
                await sm.edit("*📥 Descargando .torrent...*")
                r = requests.get(text, timeout=30)
                if r.status_code != 200:
                    await sm.edit(f"*❌ HTTP {r.status_code}*")
                    if cid in self.active_tasks:
                        del self.active_tasks[cid]
                    return
                
                filename = text.split("/")[-1]
                info = self.download_client.add_torrent_file(r.content, filename)
                
            else:
                log.info(f"Descargando URL HTTP")
                filename = text.split("/")[-1] or "descarga"
                info = self.download_client.add_http(text, filename)
                await sm.edit(f"*📥 HTTP*")
            
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ Agregado:*\n`{info['name'][:40]}`\n\n"
                f"`{self._pbar(0)}` 0%"
            )
            
            log.info(f"Iniciando: {info['download_id']}")
            self.download_client.start_download(info["download_id"])
            
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error: {e}", exc_info=True)
            await sm.edit(f"*❌ Error:* `{str(e)[:100]}`")

    async def _monitor(self, cid, download_id):
        """Monitorea descarga"""
        try:
            last_progress = -1
            no_progress_count = 0
            
            while cid in self.active_tasks:
                p = self.download_client.get_progress(download_id)
                
                if not p:
                    no_progress_count += 1
                    if no_progress_count > 120:  # 2 minutos
                        log.warning("Timeout sin progreso")
                        break
                    await asyncio.sleep(1)
                    continue
                
                no_progress_count = 0
                
                if int(p["progress"]) != last_progress:
                    last_progress = int(p["progress"])
                    await self._upd_progress(cid, p)
                    log.info(f"Progreso: {p['progress']:.1f}% - {p['status']}")
                
                if p["status"] == "completed":
                    log.info(f"✅ COMPLETADO")
                    break
                elif p["status"] in ["error", "cancelled"]:
                    log.info(f"Estado final: {p['status']}")
                    break
                
                await asyncio.sleep(1)
            
            await self._upd_msg(cid, "*✅ Completado! Subiendo...*")
            
            files = self.download_client.get_completed_files(download_id)
            if files:
                log.info(f"Archivos a subir: {len(files)}")
                await self._upload(cid, files)
            else:
                log.warning(f"Sin archivos")
                await self._upd_msg(cid, "*⚠ Sin archivos*")
                
        except Exception as e:
            log.error(f"Error monitor: {e}", exc_info=True)
            await self._upd_msg(cid, f"*❌ Error*")
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
        down = DownloadClient.format_size(p["downloaded"])
        total = DownloadClient.format_size(p["total"])
        
        try:
            await self.status_messages[cid].edit(
                f"*⬇️ {p['status'].upper()}*\n\n"
                f"`{bar}` `{p['progress']:.1f}%`\n"
                f"📊 {speed}/s\n"
                f"📥 {down} / {total}",
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
        """Sube archivos"""
        uploaded = 0
        failed = 0
        
        for file_path in files[:5]:
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
                await self._upd_msg(cid, f"*📤 {file_name[:30]}*")
                
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
                log.info(f"✅ {file_name}")
                
            except Exception as e:
                log.error(f"Upload error: {e}")
                failed += 1
            
            await asyncio.sleep(0.5)
        
        msg = f"*✅ {uploaded} archivo(s)*"
        if failed > 0:
            msg = f"*⚠️ {uploaded} OK / {failed} falló*"
        
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
    """Main"""
    try:
        await bot.start()
    except KeyboardInterrupt:
        pass
    finally:
        await bot.stop()

def main():
    """Main"""
    log.info("=" * 60)
    log.info("🚀 TeleTorrent Bot v3.3 - TORRENTS REALES")
    log.info("=" * 60)
    
    bot = TeleTorrentBot()
    
    def signal_handler(signum, frame):
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
