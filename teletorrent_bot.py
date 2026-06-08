#!/usr/bin/env python3
"""
🚀 TeleTorrent Bot v9.0 - DEFINITIVA Y COMPLETA
Transmission integrado automáticamente
Descarga torrents reales, sube a Telegram, limpia automáticamente
SIN CONFIGURACIÓN MANUAL
"""

import asyncio
import hashlib
import json
import logging
import os
import re
import signal
import sys
import time
import threading
import subprocess
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
    TRANSMISSION_USERNAME = None
    TRANSMISSION_PASSWORD = None
    TRANSMISSION_DOWNLOAD_DIR = str(Path.home() / "downloads" / "torrents")

# ═══ LOGGING ═════════════════════════════════════════════════════════════════
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    datefmt="%Y-%m-%d %H:%M:%S",
)
log = logging.getLogger("TeleTorrent")

# ═══ GESTOR DE TRANSMISSION ══════════════════════════════════════════════════
class TransmissionManager:
    """Gestor automático de Transmission"""

    @staticmethod
    def setup():
        """Configura Transmission automáticamente"""
        log.info("🔧 Configurando Transmission...")
        
        # Crear directorio de descargas
        download_dir = Path(Config.TRANSMISSION_DOWNLOAD_DIR)
        download_dir.mkdir(parents=True, exist_ok=True)
        log.info(f"✓ Directorio: {download_dir}")
        
        # Crear directorio de configuración
        config_dir = Path.home() / ".config" / "transmission-daemon"
        config_dir.mkdir(parents=True, exist_ok=True)
        
        settings_file = config_dir / "settings.json"
        
        # Configuración default
        settings = {
            "alt-speed-down": 50,
            "alt-speed-enabled": False,
            "alt-speed-time-begin": 540,
            "alt-speed-time-day": 127,
            "alt-speed-time-enabled": False,
            "alt-speed-time-end": 1020,
            "alt-speed-up": 50,
            "announce-ip": "",
            "announce-ip-enabled": False,
            "anti-ip-blocklist": False,
            "anti-ip-blocklist-url": "http://list.iblocklist.com/lists/blueeye/anti-p2p/iplist.txt",
            "blocklist-enabled": False,
            "blocklist-updates-enabled": True,
            "blocklist-url": "http://list.iblocklist.com/lists/level1/anti-p2p/iplist.txt",
            "cache-size-mb": 16,
            "compact-view": False,
            "dht-enabled": True,
            "download-dir": str(download_dir),
            "download-queue-enabled": True,
            "download-queue-size": 3,
            "encryption": 1,
            "exec-commands": "",
            "exec-commands-enabled": False,
            "exit-when-done": False,
            "incomplete-dir": "",
            "incomplete-dir-enabled": False,
            "inhibit-desktop-hibernation": False,
            "lpd-enabled": True,
            "main-window-height": 500,
            "main-window-is-maximized": False,
            "main-window-width": 900,
            "main-window-x": 50,
            "main-window-y": 50,
            "message-level": 2,
            "metadata-pause-threshold": 90,
            "metainfo-dir": "",
            "metainfo-dir-enabled": False,
            "network-bind-address-ipv4": "127.0.0.1",
            "network-bind-address-ipv6": "::1",
            "peer-congestion-algorithm": "",
            "peer-dht-enabled": True,
            "peer-exchange-enabled": True,
            "peer-id-ttl-hours": 6,
            "peer-limit-global": 240,
            "peer-limit-per-torrent": 60,
            "peer-port": 6881,
            "peer-port-random-high": 6889,
            "peer-port-random-low": 6881,
            "peer-port-random-on-start": False,
            "peer-scrobbler-enabled": False,
            "peer-socket-tos": "default",
            "pex-enabled": True,
            "port-forwarding-enabled": False,
            "preallocation": 1,
            "prefetch-enabled": True,
            "proxy": "",
            "proxy-auth-enabled": False,
            "proxy-auth-password": "",
            "proxy-auth-username": "",
            "proxy-enabled": False,
            "proxy-port": 80,
            "proxy-type": 0,
            "queue-stalled-enabled": True,
            "queue-stalled-minutes": 30,
            "ratio-limit": 2,
            "ratio-limit-enabled": False,
            "rename-partial-files": True,
            "rpc-authentication-required": False,
            "rpc-bind-address": "127.0.0.1",
            "rpc-enabled": True,
            "rpc-host-whitelist": "127.0.0.1,localhost",
            "rpc-host-whitelist-enabled": False,
            "rpc-password": "",
            "rpc-port": 9091,
            "rpc-url": "/transmission/rpc/",
            "rpc-username": "",
            "rpc-whitelist": "127.0.0.1,::1",
            "rpc-whitelist-enabled": False,
            "scrape-paused-torrents": False,
            "script-torrent-added-filename": "",
            "script-torrent-added-enabled": False,
            "script-torrent-done-filename": "",
            "script-torrent-done-enabled": False,
            "seed-queue-enabled": False,
            "seed-queue-size": 10,
            "show-backup-trackers": False,
            "sort-mode": 0,
            "sort-reversed": False,
            "speed-limit-down": 0,
            "speed-limit-down-enabled": False,
            "speed-limit-up": 0,
            "speed-limit-up-enabled": False,
            "start-added-torrents": True,
            "statusbar-stats": 0,
            "torrent-added-notification-enabled": True,
            "torrent-complete-notification-enabled": True,
            "torrent-complete-script-enabled": False,
            "torrent-complete-script-filename": "",
            "torrenting-enabled": True,
            "trash-can-enabled": True,
            "trash-original-torrent-files": False,
            "umask": 18,
            "upload-slots-per-torrent": 14,
            "utp-enabled": True,
            "watch-dir": "",
            "watch-dir-enabled": False
        }
        
        # Guardar configuración
        try:
            settings_file.write_text(json.dumps(settings, indent=4))
            os.chmod(settings_file, 0o600)
            log.info(f"✓ Configuración guardada")
        except Exception as e:
            log.warning(f"⚠ No se pudo guardar configuración: {e}")

    @staticmethod
    def is_running() -> bool:
        """Verifica si Transmission está corriendo"""
        try:
            result = subprocess.run(
                ["pgrep", "-f", "transmission-daemon"],
                capture_output=True,
                timeout=5
            )
            return result.returncode == 0
        except:
            return False

    @staticmethod
    def start():
        """Inicia Transmission daemon"""
        if TransmissionManager.is_running():
            log.info("✓ Transmission ya está corriendo")
            return
        
        log.info("🚀 Iniciando Transmission daemon...")
        
        try:
            # Matar procesos anteriores
            subprocess.run(["pkill", "-f", "transmission-daemon"], capture_output=True, timeout=5)
            time.sleep(1)
            
            # Iniciar nuevo daemon
            subprocess.Popen(
                ["transmission-daemon", "-f"],
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                start_new_session=True
            )
            
            # Esperar a que inicie
            time.sleep(3)
            
            if TransmissionManager.is_running():
                log.info("✓ Transmission iniciado correctamente")
            else:
                log.error("❌ Transmission no inició")
                sys.exit(1)
                
        except Exception as e:
            log.error(f"❌ Error iniciando Transmission: {e}")
            sys.exit(1)

    @staticmethod
    def stop():
        """Detiene Transmission daemon"""
        try:
            subprocess.run(["pkill", "-f", "transmission-daemon"], capture_output=True, timeout=5)
            log.info("✓ Transmission detenido")
        except:
            pass

# ═══ CLIENTE TRANSMISSION ════════════════════════════════════════════════════
class TransmissionClient:
    """Cliente Transmission RPC"""

    def __init__(self):
        """Inicializa cliente"""
        self.torrents: Dict = {}
        self.lock = threading.Lock()
        self.client = None
        
        # Conectar a Transmission
        self._connect()

    def _connect(self):
        """Conecta al daemon Transmission"""
        max_retries = 30
        retry_count = 0
        
        while retry_count < max_retries:
            try:
                self.client = transmissionrpc.Client(
                    host=Config.TRANSMISSION_HOST,
                    port=Config.TRANSMISSION_PORT,
                    username=Config.TRANSMISSION_USERNAME,
                    password=Config.TRANSMISSION_PASSWORD,
                    timeout=5,
                )
                
                # Verificar conexión
                session = self.client.get_session()
                log.info(f"✅ Transmission conectado")
                log.info(f"📁 Directorio: {session.download_dir}")
                return
                
            except Exception as e:
                retry_count += 1
                if retry_count < max_retries:
                    log.warning(f"⚠ Reintentando conexión ({retry_count}/{max_retries})...")
                    time.sleep(1)
                else:
                    log.error(f"❌ No se pudo conectar a Transmission después de {max_retries} intentos")
                    sys.exit(1)

    def add_magnet(self, magnet_uri: str) -> Dict:
        """Agrega magnet"""
        try:
            log.info(f"📥 Agregando magnet...")
            torrent = self.client.add_torrent(magnet_uri)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                }
            
            log.info(f"✓ Magnet: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
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
            
            torrent = self.client.add_torrent(str(temp_path))
            
            # Eliminar temporal
            temp_path.unlink(missing_ok=True)
            
            with self.lock:
                self.torrents[torrent.id] = {
                    "id": torrent.id,
                    "name": torrent.name,
                }
            
            log.info(f"✓ Torrent: {torrent.name}")
            
            return {
                "torrent_id": torrent.id,
                "name": torrent.name,
            }
            
        except Exception as e:
            log.error(f"Error: {e}")
            raise

    def get_progress(self, torrent_id: int) -> Optional[Dict]:
        """Obtiene progreso"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            
            return {
                "name": torrent.name,
                "progress": torrent.progress,
                "downloaded": torrent.downloaded,
                "total": torrent.total_size,
                "speed": torrent.rateDownload,
                "eta": torrent.eta if torrent.eta != -1 else 0,
                "peers": torrent.num_seeds,
            }
            
        except:
            return None

    def get_files(self, torrent_id: int) -> List[str]:
        """Obtiene archivos"""
        try:
            torrent = self.client.get_torrent(torrent_id)
            session = self.client.get_session()
            
            files = []
            download_dir = Path(session.download_dir)
            
            for file in torrent.files():
                file_path = download_dir / file['name']
                if file_path.exists() and file_path.stat().st_size > 0:
                    files.append(str(file_path))
            
            return files
            
        except:
            return []

    def remove_torrent(self, torrent_id: int, delete_data: bool = True):
        """Elimina torrent"""
        try:
            self.client.remove_torrent(torrent_id, delete_data=delete_data)
            
            with self.lock:
                if torrent_id in self.torrents:
                    del self.torrents[torrent_id]
            
            log.info(f"✓ Torrent eliminado")
            
        except Exception as e:
            log.warning(f"Error: {e}")

    def start_torrent(self, torrent_id: int):
        """Inicia descarga"""
        try:
            self.client.start_torrent(torrent_id)
            log.info(f"🚀 Descarga iniciada")
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
                self.client.close()
        except:
            pass

# ═══ BOT TELEGRAM ════════════════════════════════════════════════════════════
class TorrentBot:
    """Bot Telegram"""

    def __init__(self):
        # Configurar y iniciar Transmission
        TransmissionManager.setup()
        TransmissionManager.start()
        
        # Cliente Transmission
        self.transmission = TransmissionClient()
        
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
                "*🚀 TeleTorrent Bot v9.0*\n\n"
                "Transmission Integrado\n"
                "Descarga torrents reales\n\n"
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
            p = self.transmission.get_progress(tid)
            
            if not p:
                return
            
            bar = self._bar(int(p["progress"]))
            speed = TransmissionClient.format_size(int(p["speed"]))
            eta = f"{int(p['eta'])}s" if p["eta"] > 0 else "∞"
            
            await event.reply(
                f"`{bar}` {p['progress']:.0f}%\n"
                f"{speed}/s | ETA: {eta}\n"
                f"🌱 {p['peers']} peers"
            )

        @self.client.on(events.NewMessage(pattern=r"^/cancel$"))
        async def cancel_cmd(event):
            cid = event.chat_id
            
            if cid not in self.active_tasks:
                return
            
            tid = self.active_tasks[cid]
            self.transmission.remove_torrent(tid, delete_data=True)
            
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
            info = self.transmission.add_magnet(magnet)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
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
            info = self.transmission.add_torrent_file(r.content, filename)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
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
            
            info = self.transmission.add_torrent_file(data, fname)
            
            self.active_tasks[cid] = info["torrent_id"]
            
            await sm.edit(
                f"*✅ {info['name'][:40]}*\n"
                f"`{self._bar(0)}` 0%"
            )
            
            self.transmission.start_torrent(info["torrent_id"])
            
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
                p = self.transmission.get_progress(tid)
                
                if not p:
                    await asyncio.sleep(1)
                    continue
                
                if int(p["progress"]) != last_p:
                    last_p = int(p["progress"])
                    
                    bar = self._bar(int(p["progress"]))
                    speed = TransmissionClient.format_size(int(p["speed"]))
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
            
            files = self.transmission.get_files(tid)
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
            
            self.transmission.remove_torrent(tid, delete_data=True)
            
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
        self.transmission.close()
        await self.client.disconnect()
        TransmissionManager.stop()

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
    log.info("🚀 TeleTorrent Bot v9.0 - TRANSMISSION INTEGRADO")
    log.info("=" * 70)
    
    bot = TorrentBot()
    
    def signal_handler(signum, frame):
        asyncio.create_task(bot.stop())
        sys.exit(0)
    
    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)
    
    asyncio.run(main(bot))
