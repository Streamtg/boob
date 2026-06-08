#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v10.0 - CON QBITTORRENT
Descarga torrents reales, sube a Telegram, limpia automáticamente
SIN COMPLICACIONES
"""

import asyncio
import hashlib
import json
import logging
import os
import re
import signal
import sys
import subprocess
import time
import threading
from pathlib import Path
from typing import Optional, Dict, List

# ═══ IMPORTACIONES ═══════════════════════════════════════════════════════════
from telethon import TelegramClient, events
import requests

try:
    from qbittorrent import Client
except ImportError:
    print("Instalando qbittorrent...")
    subprocess.run([sys.executable, "-m", "pip", "install", "qbittorrent==2.0.0"], check=True)
    from qbittorrent import Client

# ═══ CONFIGURACION ═══════════════════════════════════════════════════════════
class Config:
    """Configuración"""
    
    # Telegram
    API_ID = 34280578
    API_HASH = "b77ac49b31b12365b98f2333bd4c3eb0"
    BOT_TOKEN = "8835976877:AAHZyBbv_6MmVSnQ5rdM4Csq8Qjrb3Zjy60"
    CHANNEL_ID = -1003213143951
    
    # qBittorrent
    QBIT_HOST = "http://127.0.0.1:8080"
    QBIT_USERNAME = "admin"
    QBIT_PASSWORD = "adminPassword"
    QBIT_DOWNLOAD_DIR = str(Path.home() / "downloads" / "torrents")

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ GESTOR QBITTORRENT ══════════════════════════════════════════════════════
class QBitManager:
    """Gestor automático de qBittorrent"""

    @staticmethod
    def start():
        """Inicia qBittorrent daemon"""
        if QBitManager.is_running():
            log.info("✓ qBittorrent ya está corriendo")
            return
        
        log.info("🚀 Iniciando qBittorrent...")
        
        try:
            # Matar procesos anteriores
            subprocess.run(["pkill", "-f", "qbittorrent"], capture_output=True, timeout=5)
            time.sleep(1)
            
            # Crear directorio
            download_dir = Path(Config.QBIT_DOWNLOAD_DIR)
            download_dir.mkdir(parents=True, exist_ok=True)
            
            # Iniciar qBittorrent
            subprocess.Popen(
                ["qbittorrent", "--webui-port=8080"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True
            )
            
            # Esperar a que inicie
            time.sleep(3)
            
            if QBitManager.is_running():
                log.info("✓ qBittorrent iniciado correctamente")
            else:
                log.error("❌ qBittorrent no inició")
                sys.exit(1)
                
        except Exception as e:
            log.error(f"❌ Error: {e}")
            sys.exit(1)

    @staticmethod
    def is_running() -> bool:
        """Verifica si qBittorrent está corriendo"""
        try:
            result = subprocess.run(
                ["pgrep", "-f", "qbittorrent"],
                capture_output=True,
                timeout=5
            )
            return result.returncode == 0
        except:
            return False

    @staticmethod
    def stop():
        """Detiene qBittorrent"""
        try:
            subprocess.run(["pkill", "-f", "qbittorrent"], capture_output=True, timeout=5)
            log.info("✓ qBittorrent detenido")
        except:
            pass

# ═══ CLIENTE QBITTORRENT ═════════════════════════════════════════════════════
class QBitClient:
    """Cliente qBittorrent"""

    def __init__(self):
        """Inicializa cliente"""
        self.torrents: Dict = {}
        self.lock = threading.Lock()
        self.client = None
        
        # Conectar
        self._connect()

    def _connect(self):
        """Conecta al cliente qBittorrent"""
        max_retries = 30
        retry_count = 0
        
        while retry_count < max_retries:
            try:
                self.client = Client(Config.QBIT_HOST)
                self.client.login(Config.QBIT_USERNAME, Config.QBIT_PASSWORD)
                
                log.info(f"✅ qBittorrent conectado")
                log.info(f"📁 Directorio: {Config.QBIT_DOWNLOAD_DIR}")
                return
                
            except Exception as e:
                retry_count += 1
                if retry_count < max_retries:
                    log.warning(f"⚠ Reintentando ({retry_count}/{max_retries})...")
                    time.sleep(1)
                else:
                    log.error(f"❌ No se pudo conectar después de {max_retries} intentos")
                    sys.exit(1)

    def add_magnet(self, magnet_uri: str) -> Dict:
        """Agrega magnet"""
        try:
            log.info(f"📥 Agregando magnet...")
            
            self.client.download_from_link(
                magnet_uri,
                savepath=Config.QBIT_DOWNLOAD_DIR
            )
            
            # Esperar a que aparezca en la lista
            time.sleep(1)
            
            torrents = self.client.torrents()
            if torrents:
                torrent = torrents[0]
                torrent_id = torrent['hash']
                name = torrent['name']
                
                with self.lock:
                    self.torrents[torrent_id] = {
                        "id": torrent_id,
                        "name": name,
                    }
                
                log.info(f"✓ Magnet: {name}")
                
                return {
                    "torrent_id": torrent_id,
                    "name": name,
                }
            
        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def add_torrent_file(self, torrent_data: bytes, filename: str) -> Dict:
        """Agrega archivo .torrent"""
        try:
            log.info(f"📥 Agregando torrent...")
            
            # Guardar temporalmente
            temp_path = Path("/tmp") / f"temp_{int(time.time())}.torrent"
            temp_path.write_bytes(torrent_data)
            
            # Agregar torrent
            self.client.download_from_file(
                open(temp_path, 'rb'),
                savepath=Config.QBIT_DOWNLOAD_DIR
            )
            
            # Eliminar temporal
            temp_path.unlink(missing_ok=True)
            
            # Esperar a que aparezca
            time.sleep(1)
            
            torrents = self.client.torrents()
            if torrents:
                torrent = torrents[0]
                torrent_id = torrent['hash']
                name = torrent['name']
                
                with self.lock:
                    self.torrents[torrent_id] = {
                        "id": torrent_id,
                        "name": name,
                    }
                
                log.info(f"✓ Torrent: {name}")
                
                return {
                    "torrent_id": torrent_id,
                    "name": name,
                }
            
        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def get_progress(self, torrent_id: str) -> Optional[Dict]:
        """Obtiene progreso"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            
            if not torrent:
                return None
            
            return {
                "name": torrent['name'],
                "progress": torrent['progress'] * 100,
                "downloaded": torrent['downloaded'],
                "total": torrent['total_size'],
                "speed": torrent['dl_speed'],
                "eta": torrent['eta'],
                "peers": torrent['num_seeds'],
            }
            
        except:
            return None

    def get_files(self, torrent_id: str) -> List[str]:
        """Obtiene archivos"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            
            if not torrent:
                return []
            
            files = []
            download_dir = Path(Config.QBIT_DOWNLOAD_DIR)
            
            try:
                file_list = self.client.get_torrent_files(torrent_id)
                for file in file_list:
                    file_path = download_dir / file['name']
                    if file_path.exists() and file_path.stat().st_size > 0:
                        files.append(str(file_path))
            except:
                # Si no funciona, buscar en directorio
                for item in download_dir.rglob("*"):
                    if item.is_file() and item.stat().st_size > 0:
                        files.append(str(item))
            
            return files
            
        except:
            return []

    def remove_torrent(self, torrent_id: str, delete_data: bool = True):
        """Elimina torrent"""
        try:
            if delete_data:
                self.client.delete_permanently(torrent_id)
            else:
                self.client.delete(torrent_id)
            
            with self.lock:
                if torrent_id in self.torrents:
                    del self.torrents[torrent_id]
            
            log.info(f"✓ Torrent eliminado")
            
        except Exception as e:
            log.warning(f"Error: {e}")

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
        """Cierra"""
        try:
            if self.client:
                self.client.logout()
        except:
            pass

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TorrentBot:
    """Bot Telegram"""

    def __init__(self):
        # Iniciar qBittorrent
        QBitManager.start()
        
        # Cliente qBittorrent
        self.qbit = QBitClient()
        
        # Control
        self.active_tasks: Dict = {}
        self.status_msgs: Dict = {}
        
        # Cliente Telegram
        self.client = TelegramClient(
            "bot_session",
            Config.API_ID,
            Config.API_HASH,
            connection_retries=5,
            timeout=30
        )
        
        log.info("✅ Bot inicializado")

    async def start(self):
        """Inicia bot"""
        try:
            log.info("🔌 Conectando a Telegram...")
            await self.client.start(bot_token=Config.BOT_TOKEN)
            
            me = await self.client.get_me()
            log.info(f"✓ Bot: @{me.username}")
            
            try:
                ch = await self.client.get_entity(Config.CHANNEL_ID)
                log.info(f"✓ Canal: {ch.title}")
            except:
                pass
            
            self._handlers()
            
            log.info("🔍 Escuchando...")
            await self.client.run_until_disconnected()
            
        except Exception as e:
            log.error(f"Error: {e}")

    def _handlers(self):
        """Handlers"""
        
        @self.client.on(events.NewMessage(pattern=r"^/start$|^/help$"))
        async def help_cmd(event):
            await event.reply(
                "*🚀 TeleTorrent Bot v10.0*\n\n"
                "qBittorrent integrado\n\n"
                "*Comandos:*\n"
                "/status - Ver progreso\n"
                "/cancel - Cancelar\n\n"
                "*Envía:*\n"
                "• Magnet link\n"
                "• Archivo .torrent\n"
                "• URL .torrent",
                parse_mode="markdown"
            )

        @self.client.on(events.NewMessage(pattern=r"^/status$"))
        async def status_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                await event.reply("*Sin descargas*")
                return
            
            tid = self.active_tasks[cid]
            p = self.qbit.get_progress(tid)
            
            if not p:
                return
            
            bar = self._bar(int(p["progress"]))
            speed = QBitClient.format_size(int(p["speed"]))
            eta = f"{int(p['eta'])}s" if p["eta"] > 0 else "∞"
            
            await event.reply(
                f"`{bar}` {p['progress']:.0f}%\n"
                f"{speed}/s | ETA: {eta}\n"
                f"🌱 {p['peers']} seeds"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                return
            
            tid = self.active_tasks[cid]
            self.qbit.remove_torrent(tid, delete_data=True)
            
            del self.active_tasks[cid]
            
            if cid in self.status_msgs:
                try:
                    await self.status_msgs[cid].delete()
                except:
                    pass
                del self.status_msgs[cid]

        @self.client.on(events.NewMessage)
        async def msg_handler(event):
            text = (event.message.text or "").strip()
            
            if text.startswith("/"):
                return
            
            if text.startswith("magnet:"):
                await self._magnet_download(event, text)
                return
            
            if text.endswith(".torrent"):
                await self._url_torrent_download(event, text)
                return
            
            if event.message.document:
                await self._file_torrent(event)

    async def _magnet_download(self, event, magnet: str):
        """Descarga magnet"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Procesando magnet...*")
        self.status_msgs[cid] = sm
        
        try:
            info = self.qbit.add_magnet(magnet)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _url_torrent_download(self, event, url: str):
        """Descarga .torrent desde URL"""
        cid = event.chat_id
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Descargando .torrent...*")
        self.status_msgs[cid] = sm
        
        try:
            r = requests.get(url, timeout=30)
            if r.status_code != 200:
                await sm.edit("*❌ Error*")
                return
            
            filename = url.split("/")[-1]
            info = self.qbit.add_torrent_file(r.content, filename)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _file_torrent(self, event):
        """Archivo .torrent"""
        if not event.message.document:
            return
        
        cid = event.chat_id
        fname = event.message.document.file_name or "torrent.torrent"
        
        if not fname.lower().endswith(".torrent"):
            return
        
        if cid in self.active_tasks:
            await event.reply("*Ya hay descarga*")
            return
        
        sm = await event.reply("*⏳ Procesando...*")
        self.status_msgs[cid] = sm
        
        try:
            data = await event.message.download_media(bytes)
            if not data:
                await sm.edit("*❌ Error*")
                return
            
            info = self.qbit.add_torrent_file(data, fname)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            asyncio.create_task(self._monitor(cid))
            
        except Exception as e:
            log.error(f"Error: {e}")
            await sm.edit("*❌ Error*")

    async def _monitor(self, cid: int):
        """Monitorea"""
        try:
            if cid not in self.active_tasks:
                return
            
            tid = self.active_tasks[cid]
            last_p = -1
            
            while cid in self.active_tasks:
                p = self.qbit.get_progress(tid)
                
                if not p:
                    await asyncio.sleep(1)
                    continue
                
                if int(p["progress"]) != last_p:
                    last_p = int(p["progress"])
                    
                    bar = self._bar(int(p["progress"]))
                    speed = QBitClient.format_size(int(p["speed"]))
                    eta = f"{int(p['eta'])}s" if p["eta"] > 0 else "∞"
                    
                    try:
                        if cid in self.status_msgs:
                            await self.status_msgs[cid].edit(
                                f"`{bar}` {p['progress']:.0f}%\n"
                                f"{speed}/s | ETA: {eta}\n"
                                f"🌱 {p['peers']}"
                            )
                    except:
                        pass
                
                if p["progress"] >= 100:
                    break
                
                await asyncio.sleep(1)
            
            # Upload
            try:
                await self.status_msgs[cid].edit("*✅ Subiendo...*")
            except:
                pass
            
            files = self.qbit.get_files(tid)
            ok = 0
            
            for fpath in files[:5]:
                try:
                    fname = os.path.basename(fpath)
                    
                    await self.client.send_file(
                        Config.CHANNEL_ID,
                        file=fpath,
                        caption=fname,
                        force_document=True
                    )
                    
                    try:
                        os.remove(fpath)
                        log.info(f"🗑️ Eliminado: {fname}")
                    except:
                        pass
                    
                    ok += 1
                    await asyncio.sleep(0.5)
                except Exception as e:
                    log.error(f"Upload: {e}")
            
            try:
                await self.client.send_message(
                    cid,
                    f"*✅ {ok} archivo(s) subido(s)*"
                )
            except:
                pass
            
            self.qbit.remove_torrent(tid, delete_data=True)
            
        except Exception as e:
            log.error(f"Monitor: {e}")
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

    @staticmethod
    def _bar(p: int) -> str:
        """Barra"""
        filled = min(int(p / 5), 20)
        return "█" * filled + "░" * (20 - filled)

    async def stop(self):
        """Detiene"""
        log.info("🛑 Deteniendo...")
        self.qbit.close()
        await self.client.disconnect()
        QBitManager.stop()

# ═══ MAIN ════════════════════════════════════════════════════════════════════
async def main(bot):
    try:
        await bot.start()
    except KeyboardInterrupt:
        pass
    finally:
        await bot.stop()

if __name__ == "__main__":
    log.info("=" * 70)
    log.info("🚀 TeleTorrent Bot v10.0 - QBITTORRENT")
    log.info("=" * 70)
    
    bot = TorrentBot()
    
    def signal_handler(signum, frame):
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    try:
        asyncio.run(main(bot))
    except Exception as e:
        log.error(f"Error: {e}")
        sys.exit(1)
