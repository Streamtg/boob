#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v4.0 - PROFESIONAL
Descarga torrents REALES, sube a Telegram y limpia automáticamente
Solución DEFINITIVA y completamente funcional
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
import threading
from pathlib import Path
from typing import Optional, Dict, List
from urllib.parse import quote, unquote

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

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
    """Configuración centralizada"""
    
    # Credenciales
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    
    # Rutas
    STORAGE_PATH = "./downloads"
    MAX_WORKERS = 2
    CHUNK_SIZE = 5 * 1024 * 1024

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ CLIENTE DE DESCARGA PROFESIONAL ═════════════════════════════════════════
class DownloadManager:
    """Gestor de descargas profesional con aria2c"""

    def __init__(self, storage_path: str):
        self.storage_path = Path(storage_path)
        self.storage_path.mkdir(parents=True, exist_ok=True)
        
        self.torrent_path = self.storage_path / "torrents"
        self.torrent_path.mkdir(exist_ok=True)
        
        self.downloads: Dict = {}
        self.processes: Dict = {}
        self.download_lock = threading.Lock()
        
        # Verificar aria2c
        if not self._check_aria2():
            log.error("❌ aria2c no disponible. Instala: sudo apt-get install -y aria2")
            sys.exit(1)
        
        log.info("✅ Sistema de descargas inicializado")

    @staticmethod
    def _check_aria2() -> bool:
        """Verifica aria2c"""
        try:
            result = subprocess.run(
                ["aria2c", "--version"],
                capture_output=True,
                timeout=5
            )
            if result.returncode == 0:
                version = result.stdout.decode().strip().split('\n')[0]
                log.info(f"📌 {version}")
                return True
        except:
            pass
        return False

    def add_magnet(self, magnet_uri: str) -> Dict:
        """Agrega magnet link"""
        try:
            magnet_uri = magnet_uri.strip()
            
            # Limpiar magnet
            if "&" in magnet_uri:
                magnet_uri = magnet_uri.split("&")[0]
            
            if not magnet_uri.startswith("magnet:?xt="):
                raise ValueError("Magnet inválido")
            
            # Extraer nombre
            dn_match = re.search(r"dn=([^&]+)", magnet_uri)
            name = dn_match.group(1) if dn_match else f"magnet_{int(time.time())}"
            
            try:
                name = unquote(name)
            except:
                pass
            
            # Limpiar nombre
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)[:60]
            
            download_id = hashlib.md5(magnet_uri.encode()).hexdigest()[:8]
            
            with self.download_lock:
                self.downloads[download_id] = {
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
            return {"download_id": download_id, "name": name}
            
        except Exception as e:
            log.error(f"Error magnet: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes, name: str) -> Dict:
        """Agrega archivo torrent"""
        try:
            name = name.replace(".torrent", "").strip()[:60]
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)
            
            if not name:
                name = f"torrent_{int(time.time())}"
            
            # Guardar torrent
            torrent_path = self.torrent_path / f"{name}.torrent"
            
            if torrent_path.exists():
                torrent_path = self.torrent_path / f"{name}_{int(time.time())}.torrent"
            
            torrent_path.write_bytes(torrent_data)
            log.info(f"📁 Torrent guardado: {torrent_path.name}")
            
            download_id = hashlib.md5(torrent_data).hexdigest()[:8]
            
            with self.download_lock:
                self.downloads[download_id] = {
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
            
            log.info(f"✓ Torrent agregado: {name}")
            return {"download_id": download_id, "name": name}
            
        except Exception as e:
            log.error(f"Error torrent: {e}")
            raise

    def add_http(self, url: str, name: str) -> Dict:
        """Agrega descarga HTTP"""
        try:
            name = name.split("?")[0].split("#")[0].split("/")[-1][:60]
            name = re.sub(r'[^a-zA-Z0-9._\- ]', '_', name)
            
            if not name:
                name = "descarga"
            
            download_id = hashlib.md5(url.encode()).hexdigest()[:8]
            
            with self.download_lock:
                self.downloads[download_id] = {
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
            
            log.info(f"✓ URL agregada: {name}")
            return {"download_id": download_id, "name": name}
            
        except Exception as e:
            log.error(f"Error HTTP: {e}")
            raise

    def start(self, download_id: str) -> bool:
        """Inicia descarga"""
        if download_id not in self.downloads:
            return False
        
        try:
            thread = threading.Thread(
                target=self._download_thread,
                args=(download_id,),
                daemon=True
            )
            thread.start()
            return True
        except Exception as e:
            log.error(f"Error iniciando: {e}")
            return False

    def _download_thread(self, download_id: str):
        """Thread de descarga"""
        if download_id not in self.downloads:
            return
        
        d = self.downloads[download_id]
        d["status"] = "downloading"
        
        try:
            if d["type"] == "http":
                self._download_http(download_id)
            else:
                self._download_aria2(download_id)
        except Exception as e:
            log.error(f"Error descargando: {e}")
            d["status"] = "error"

    def _download_http(self, download_id: str):
        """Descarga HTTP"""
        d = self.downloads[download_id]
        url = d["uri"]
        file_path = self.storage_path / d["name"]
        
        try:
            # Obtener tamaño
            try:
                r = requests.head(url, timeout=10, allow_redirects=True)
                total = int(r.headers.get("content-length", 0))
                d["total"] = total
            except:
                pass
            
            # Descargar
            r = requests.get(url, timeout=60, stream=True)
            r.raise_for_status()
            
            downloaded = 0
            start = time.time()
            
            with open(file_path, "wb") as f:
                for chunk in r.iter_content(chunk_size=Config.CHUNK_SIZE):
                    if d["status"] == "cancelled":
                        file_path.unlink(missing_ok=True)
                        return
                    
                    if chunk:
                        f.write(chunk)
                        downloaded += len(chunk)
                        d["downloaded"] = downloaded
                        
                        if d["total"] > 0:
                            d["progress"] = min(100, (downloaded / d["total"]) * 100)
                        
                        elapsed = time.time() - start
                        if elapsed > 0:
                            d["speed"] = downloaded / elapsed
            
            d["status"] = "completed"
            d["progress"] = 100
            log.info(f"✅ Completado: {d['name']}")
            
        except Exception as e:
            log.error(f"Error HTTP: {e}")
            d["status"] = "error"
            try:
                file_path.unlink(missing_ok=True)
            except:
                pass

    def _download_aria2(self, download_id: str):
        """Descarga con aria2c - VERSIÓN CORRECTA"""
        d = self.downloads[download_id]
        
        try:
            # Comandos VÁLIDOS para aria2c 1.37
            cmd = [
                "aria2c",
                "--max-concurrent-downloads=1",
                "--max-connection-per-server=4",
                "--split=4",
                f"--dir={self.storage_path}",
                "--enable-dht=true",
                "--dht-listen-port=6881-6889",
                "--enable-peer-exchange=true",
                "--bt-tracker-connect-timeout=10",
                "--continue=true",
                "--allow-overwrite=true",
                "--summary-interval=1",
            ]
            
            if d["type"] == "magnet":
                log.info(f"🚀 Descargando magnet: {d['name']}")
                cmd.append(d["uri"])
            elif d["type"] == "torrent":
                if not os.path.exists(d["path"]):
                    log.error(f"Torrent no encontrado: {d['path']}")
                    d["status"] = "error"
                    return
                log.info(f"🚀 Descargando torrent: {d['name']}")
                cmd.append(d["path"])
            
            log.debug(f"Ejecutando: {' '.join(cmd[:5])}...")
            
            # Ejecutar aria2c
            proc = subprocess.Popen(
                cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                text=True
            )
            
            self.processes[download_id] = proc
            
            # Monitorear output
            def read_output():
                try:
                    for line in proc.stdout:
                        if line and d["status"] == "downloading":
                            log.debug(f"[aria2] {line.strip()[:100]}")
                            
                            # Buscar progreso
                            if "%" in line:
                                try:
                                    m = re.search(r"(\d+)%", line)
                                    if m:
                                        d["progress"] = float(m.group(1))
                                except:
                                    pass
                except:
                    pass
            
            thread = threading.Thread(target=read_output, daemon=True)
            thread.start()
            
            # Esperar
            returncode = proc.wait()
            
            if returncode == 0:
                d["status"] = "completed"
                d["progress"] = 100
                log.info(f"✅ Completado: {d['name']}")
            else:
                d["status"] = "error"
                log.error(f"aria2c error (código {returncode})")
            
            if download_id in self.processes:
                del self.processes[download_id]
                
        except Exception as e:
            log.error(f"Error aria2: {e}")
            d["status"] = "error"
            if download_id in self.processes:
                del self.processes[download_id]

    def get_progress(self, download_id: str) -> Optional[Dict]:
        """Obtiene progreso"""
        if download_id not in self.downloads:
            return None
        
        d = self.downloads[download_id]
        return {
            "progress": d["progress"],
            "downloaded": d["downloaded"],
            "total": d["total"],
            "speed": d["speed"],
            "status": d["status"],
            "elapsed": time.time() - d["started_at"],
        }

    def cancel(self, download_id: str):
        """Cancela descarga"""
        if download_id in self.downloads:
            d = self.downloads[download_id]
            d["status"] = "cancelled"
            
            if download_id in self.processes:
                try:
                    self.processes[download_id].terminate()
                    self.processes[download_id].wait(timeout=5)
                except:
                    try:
                        self.processes[download_id].kill()
                    except:
                        pass
                del self.processes[download_id]
            
            log.info(f"✗ Cancelado: {d['name']}")

    def get_files(self, download_id: str) -> List[str]:
        """Obtiene archivos descargados"""
        files = []
        try:
            for item in self.storage_path.rglob("*"):
                if (item.is_file() and item.stat().st_size > 0 and
                    not item.name.endswith(".torrent") and
                    not item.name.startswith(".") and
                    ".aria2" not in str(item)):
                    files.append(str(item))
        except:
            pass
        
        return files[:10]

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
        """Cierra procesos"""
        log.info("🛑 Cerrando descarga...")
        for proc in self.processes.values():
            try:
                proc.terminate()
            except:
                pass

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TorrentBot:
    """Bot Telegram profesional"""

    def __init__(self):
        self.config = Config()
        self.storage = Path(self.config.STORAGE_PATH)
        self.storage.mkdir(exist_ok=True)
        
        self.dm = DownloadManager(str(self.storage))
        self.active_tasks: Dict = {}
        self.status_msgs: Dict = {}
        
        self.client = TelegramClient(
            "bot_session",
            self.config.API_ID,
            self.config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✅ Bot inicializado")

    async def start(self):
        """Inicia bot"""
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
            
            self._handlers()
            
            log.info("🔍 Escuchando...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error: {e}", exc_info=True)

    def _handlers(self):
        """Registra handlers"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_cmd(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v4.0*\n\n"
                "Descarga torrents + sube a Telegram\n\n"
                "*Comandos:*\n"
                "/status - Ver progreso\n"
                "/cancel - Cancelar\n\n"
                "*Envía:*\n"
                "• Magnet link\n"
                "• .torrent file\n"
                "• URL .torrent\n"
                "• Link HTTP",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_cmd(event):
            cid = event.chat_id
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas*", parse_mode="markdown")
                return
            
            p = self.dm.get_progress(self.active_tasks[cid])
            if not p:
                await event.reply("*N/A*", parse_mode="markdown")
                return
            
            bar = self._bar(int(p["progress"]))
            await event.reply(
                f"`{bar}` {p['progress']:.0f}%\n"
                f"{self.dm.format_size(p['speed'])}/s",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id
            if cid not in self.active_tasks:
                await event.reply("*Nada que cancelar*", parse_mode="markdown")
                return
            
            self.dm.cancel(self.active_tasks[cid])
            del self.active_tasks[cid]
            
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]
            
            await event.reply("*Cancelado*", parse_mode="markdown")

        @self.client.on(events.NewMessage)
        async def msg_handler(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            # URL o magnet
            if text.startswith(("magnet:", "http://", "https://", "ftp://")):
                log.info(f"📌 Link: {text[:50]}")
                await self._download(event, text)
                return
            
            # Archivo adjunto
            if event.message.document:
                log.info(f"📌 Archivo adjunto")
                await self._torrent_file(event)

    async def _torrent_file(self, event):
        """Maneja archivo .torrent"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        doc = event.message.document
        fname = doc.file_name or "torrent.torrent"
        
        is_torrent = (
            "torrent" in (doc.mime_type or "").lower() or
            fname.lower().endswith(".torrent")
        )
        
        if not is_torrent:
            await event.reply("*Solo .torrent*", parse_mode="markdown")
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            data = await event.message.download_media(bytes)
            if not data:
                await sm.edit("*❌ Error*")
                return
            
            info = self.dm.add_torrent_file(data, fname)
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.dm.start(info["download_id"])
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ {str(e)[:50]}*")

    async def _download(self, event, text):
        """Inicia descarga"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*", parse_mode="markdown")
            return
        
        sm = await event.reply("*⏳ Procesando...*", parse_mode="markdown")
        self.status_msgs[cid] = sm
        
        try:
            if text.startswith("magnet:"):
                info = self.dm.add_magnet(text)
            elif text.endswith(".torrent"):
                await sm.edit("*⏳ Descargando .torrent...*")
                r = requests.get(text, timeout=30)
                if r.status_code != 200:
                    await sm.edit(f"*❌ Error {r.status_code}*")
                    return
                info = self.dm.add_torrent_file(r.content, text.split("/")[-1])
            else:
                fname = text.split("/")[-1] or "descarga"
                info = self.dm.add_http(text, fname)
            
            self.active_tasks[cid] = info["download_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.dm.start(info["download_id"])
            asyncio.create_task(self._monitor(cid, info["download_id"]))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit(f"*❌ {str(e)[:50]}*")

    async def _monitor(self, cid, did):
        """Monitorea descarga"""
        try:
            last_p = -1
            
            while cid in self.active_tasks:
                p = self.dm.get_progress(did)
                if not p:
                    await asyncio.sleep(1)
                    continue
                
                if int(p["progress"]) != last_p:
                    last_p = int(p["progress"])
                    await self._update(cid, p)
                
                if p["status"] in ["completed", "error", "cancelled"]:
                    break
                
                await asyncio.sleep(1)
            
            # Descarga completada
            await self._msg(cid, "*✅ Completado! Subiendo...*")
            
            files = self.dm.get_files(did)
            if files:
                await self._upload(cid, files)
            else:
                await self._msg(cid, "*⚠ Sin archivos*")
                
        except Exception as e:
            log.error(f"Monitor error: {e}")
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

    async def _update(self, cid, p):
        """Actualiza progreso"""
        if cid not in self.status_msgs:
            return
        
        bar = self._bar(int(p["progress"]))
        speed = DownloadManager.format_size(p["speed"])
        
        try:
            await self.status_msgs[cid].edit(
                f"`{bar}` {p['progress']:.0f}%\n"
                f"{speed}/s"
            )
        except:
            pass

    async def _msg(self, cid, text):
        """Actualiza mensaje"""
        if cid in self.status_msgs:
            try:
                await self.status_msgs[cid].edit(text)
            except:
                pass

    async def _upload(self, cid, files):
        """Sube archivos y limpia"""
        ok = 0
        fail = 0
        
        for fpath in files[:5]:
            if not os.path.exists(fpath):
                continue
            
            try:
                size = os.path.getsize(fpath)
                if size == 0:
                    continue
                
                fname = os.path.basename(fpath)
                
                await self._msg(cid, f"*📤 {fname[:30]}*")
                
                # Subir archivo
                await self.client.send_file(
                    self.config.CHANNEL_ID,
                    file=fpath,
                    caption=fname,
                    force_document=True
                )
                
                ok += 1
                log.info(f"✅ {fname}")
                
                # ELIMINAR ARCHIVO DESPUÉS DE SUBIR
                try:
                    os.remove(fpath)
                    log.info(f"🗑️ Eliminado: {fname}")
                except Exception as e:
                    log.warning(f"No se pudo eliminar: {e}")
                
            except Exception as e:
                log.error(f"Upload error: {e}")
                fail += 1
            
            await asyncio.sleep(0.5)
        
        # Mensaje final
        if ok > 0:
            msg = f"*✅ {ok} archivo(s) subido(s)*"
        else:
            msg = f"*❌ Error en upload*"
        
        if fail > 0:
            msg = f"*⚠ {ok} OK / {fail} falló*"
        
        try:
            await self.client.send_message(cid, msg, parse_mode="markdown")
        except:
            pass

    @staticmethod
    def _bar(p: int) -> str:
        """Barra de progreso"""
        filled = min(int(p / 5), 20)
        return "█" * filled + "░" * (20 - filled)

    async def stop(self):
        """Detiene bot"""
        log.info("🛑 Deteniendo...")
        self.dm.close()
        await self.client.disconnect()
        log.info("✓ Detenido")

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    try:
        await bot.start()
    except KeyboardInterrupt:
        pass
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 60)
    log.info("🚀 TeleTorrent Bot v4.0 - PROFESIONAL")
    log.info("=" * 60)
    
    bot = TorrentBot()
    
    def signal_handler(signum, frame):
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main(bot))
    except Exception as e:
        log.error(f"Fatal: {e}")
